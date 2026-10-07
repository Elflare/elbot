package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/llm/responses"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func observerSignals() agentevents.Signals {
	return agentevents.Signals{
		UserInputReceived:   signal.New[agentevents.UserInputReceivedEvent]("input"),
		PersistenceFailed:   signal.New[agentevents.PersistenceFailedEvent]("persistence"),
		TurnTimedOut:        signal.New[agentevents.TurnTimedOutEvent]("timeout"),
		ModelCallCompleted:  signal.New[agentevents.ModelCallCompletedEvent]("model"),
		ToolCallCompleted:   signal.New[agentevents.ToolCallCompletedEvent]("tool"),
		ConfirmationChanged: signal.New[agentevents.ConfirmationChangedEvent]("confirmation"),
		ToolDenied:          signal.New[agentevents.ToolDeniedEvent]("denied"),
		StatusChanged:       signal.New[agentevents.StatusChangedEvent]("status"),
		VisionFallbackUsed:  signal.New[agentevents.VisionFallbackUsedEvent]("vision"),
		HookFailed:          signal.New[agentevents.HookFailedEvent]("hook"),
		ReplyDelivered:      signal.New[agentevents.ReplyDeliveredEvent]("delivery"),
		ReplyCommitted:      signal.New[agentevents.ReplyCommittedEvent]("commit"),
	}
}

type recordHandler struct {
	records chan slog.Record
	fail    bool
}

func (h recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.records <- r.Clone()
	if h.fail {
		return errors.New("disk write failed")
	}
	return nil
}
func (h recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordHandler) WithGroup(string) slog.Handler      { return h }
func recordAttrs(record slog.Record) map[string]any {
	attrs := map[string]any{}
	record.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Any(); return true })
	return attrs
}
func awaitRecord(t *testing.T, records <-chan slog.Record) slog.Record {
	t.Helper()
	select {
	case record := <-records:
		return record
	case <-time.After(time.Second):
		t.Fatal("missing log record")
		return slog.Record{}
	}
}

func TestAgentLoggingSubscribersPreserveFactsAndFields(t *testing.T) {
	runtimeRecords, auditRecords := make(chan slog.Record, 30), make(chan slog.Record, 30)
	b, events := &signalBindings{}, observerSignals()
	if err := b.connectAgentLogs(events, slog.New(recordHandler{records: runtimeRecords}), slog.New(recordHandler{records: auditRecords})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "other-session", RunID: "other-run", Attempt: "other-attempt", RequestID: "other-request", RootRequestID: "other-root"}))
	cancel() // Facts remain consumable after their request is finished.
	meta := agentevents.EventMeta{At: time.Unix(123, 0), SessionID: "s", RunID: "r", Attempt: "a", RequestID: "req", RootRequestID: "root"}
	nextRecord := func(records <-chan slog.Record) slog.Record {
		t.Helper()
		record := awaitRecord(t, records)
		want := map[string]string{"session_id": meta.SessionID, "run_id": meta.RunID, "attempt": meta.Attempt, "request_id": meta.RequestID, "root_request_id": meta.RootRequestID}
		counts := map[string]int{}
		record.Attrs(func(attr slog.Attr) bool {
			if value, ok := want[attr.Key]; ok {
				counts[attr.Key]++
				if attr.Value.String() != value {
					t.Errorf("%s: %s=%s, want %s", record.Message, attr.Key, attr.Value.String(), value)
				}
			}
			return true
		})
		for key := range want {
			if counts[key] != 1 {
				t.Errorf("%s: %s occurred %d times", record.Message, key, counts[key])
			}
		}
		return record
	}
	model := agentevents.ModelCallCompletedEvent{EventMeta: meta, Provider: "p", Model: "m", OutputReady: true, Text: "rewritten", SourceText: "source", Usage: &llm.Usage{TotalTokens: 42}, ElapsedMS: 8}
	if err := events.ModelCallCompleted.Emit(ctx, model); err != nil {
		t.Fatal(err)
	}
	if err := events.ToolCallCompleted.Emit(ctx, agentevents.ToolCallCompletedEvent{EventMeta: meta, Arguments: " {\"x\": 1} ", Record: storage.ToolCallRecord{ToolName: "tool", ToolCallID: "id", ActorID: "actor", RiskLevel: "low", Success: true, ResultPreview: "done"}, RecordErr: errors.New("record failed")}); err != nil {
		t.Fatal(err)
	}
	if err := events.ConfirmationChanged.Emit(ctx, agentevents.ConfirmationChangedEvent{EventMeta: meta, Phase: "result", Action: "reject", Tool: "tool", Risk: "high", Reason: "no"}); err != nil {
		t.Fatal(err)
	}
	if err := events.ReplyCommitted.Emit(ctx, agentevents.ReplyCommittedEvent{EventMeta: meta, Persisted: true, MessageID: "msg", AssociationErrors: []error{agentevents.AssociationFailure{PlatformMessageID: "sent", Err: errors.New("mapping failed")}}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := nextRecord(runtimeRecords)
	attrs := recordAttrs(first)
	if first.Message != "llm output" || !first.Time.Equal(meta.At) || attrs["text"] != "rewritten" || attrs["raw_text"] != "source" {
		t.Fatalf("model output: %+v %v", first, attrs)
	}
	if r := nextRecord(runtimeRecords); r.Message != "record tool call failed" {
		t.Fatal(r)
	}
	tool := nextRecord(runtimeRecords)
	if attrs := recordAttrs(tool); tool.Message != "tool call" || attrs["success"] != true || attrs["arguments"] != "{\"x\":1}" {
		t.Fatalf("record failure changed execution success: %v", attrs)
	}
	if r := nextRecord(runtimeRecords); r.Message != "map platform message failed" {
		t.Fatal(r)
	}
	commit := nextRecord(runtimeRecords)
	if recordAttrs(commit)["persisted"] != true {
		t.Fatal("association failure erased successful persistence")
	}
	for _, name := range []string{"llm_usage", "tool_call", "risk_confirmation_result", "persistence_error"} {
		r := nextRecord(auditRecords)
		if recordAttrs(r)["event"] != name || !r.Time.Equal(meta.At) {
			t.Fatalf("audit order/fields: %v", recordAttrs(r))
		}
	}
	if len(runtimeRecords) != 0 || len(auditRecords) != 0 {
		t.Fatal("duplicate observations")
	}
}

func TestAgentLoggingResponseErrorDetails(t *testing.T) {
	records := make(chan slog.Record, 2)
	logs := agentLogger{audit: slog.New(recordHandler{records: records})}
	apiErr := &responses.APIError{Code: "server_error", Message: "upstream failed", EventType: "error", Detail: `{"error":{"description":"gateway failure"}}`}
	meta := agentevents.EventMeta{SessionID: "session", RequestID: "request", RunID: "run", Attempt: "attempt", RootRequestID: "root", At: time.Unix(123, 0)}
	err := fmt.Errorf("model call: %w", apiErr)
	if logErr := logs.modelAudit(context.Background(), agentevents.ModelCallCompletedEvent{EventMeta: meta, Provider: "provider", Model: "model", ElapsedMS: 61000, ProviderError: true, Err: err}); logErr != nil {
		t.Fatal(logErr)
	}
	attrs := recordAttrs(awaitRecord(t, records))
	for key, want := range map[string]any{"event": "llm_error", "session_id": "session", "request_id": "request", "run_id": "run", "attempt": "attempt", "root_request_id": "root", "provider": "provider", "model": "model", "elapsed_ms": int64(61000), "upstream_event": "error", "upstream_detail": apiErr.Detail, "error": err.Error()} {
		if attrs[key] != want {
			t.Errorf("%s=%v want=%v", key, attrs[key], want)
		}
	}
	if strings.Contains(err.Error(), "gateway failure") {
		t.Fatal("raw diagnostics entered user-facing error")
	}
	if err := logs.modelAudit(context.Background(), agentevents.ModelCallCompletedEvent{ProviderError: true, Err: errors.New("transport failed")}); err != nil {
		t.Fatal(err)
	}
	attrs = recordAttrs(awaitRecord(t, records))
	if _, ok := attrs["upstream_detail"]; ok || attrs["error"] != "transport failed" {
		t.Fatalf("plain error changed: %v", attrs)
	}
}

func TestAgentLoggingOmitsMissingIdentity(t *testing.T) {
	records := make(chan slog.Record, 2)
	logger := slog.New(recordHandler{records: records})
	logs := agentLogger{runtime: logger, audit: logger}
	ctx := contextinfo.WithExecution(t.Context(), contextinfo.Execution{SessionID: "current", RunID: "current", Attempt: "current", RequestID: "current", RootRequestID: "current"})
	meta := agentevents.EventMeta{At: time.Unix(123, 0)}
	if err := logs.userInput(ctx, agentevents.UserInputReceivedEvent{EventMeta: meta, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := logs.persistence(ctx, agentevents.PersistenceFailedEvent{EventMeta: meta, Operation: "save", Err: errors.New("failed")}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		record := awaitRecord(t, records)
		attrs := recordAttrs(record)
		for _, key := range []string{"session_id", "run_id", "attempt", "request_id", "root_request_id"} {
			if value, ok := attrs[key]; ok {
				t.Errorf("%s: absent identity %s was filled with %v", record.Message, key, value)
			}
		}
	}
}

type observerTestLogs struct {
	LogManager
	runtime *slog.Logger
}

func (l observerTestLogs) Runtime() *slog.Logger { return l.runtime }

func TestProductionAssemblyObservesConversationExactlyOnce(t *testing.T) {
	req, _, _ := runtimeAssemblyFixture(t)
	records := make(chan slog.Record, 512)
	logger := slog.New(recordHandler{records: records})
	req.Foundation.Logger = logger
	req.Foundation.Logs = observerTestLogs{LogManager: req.Foundation.Logs, runtime: logger}
	runtime, err := (defaultRuntimeFactory{}).Build(context.Background(), req)
	t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Agent.HandleMessage(context.Background(), "observe this"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Signals.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for len(records) > 0 {
		counts[(<-records).Message]++
	}
	for _, message := range []string{"user input", "llm output", "reply delivered", "reply committed"} {
		if counts[message] != 1 {
			t.Fatalf("%s: %d records", message, counts[message])
		}
	}
}

func TestLogBackpressureUsesConsumerAndContinuesAfterWriteFailure(t *testing.T) {
	q, err := signal.NewQueue(signal.QueueOptions{Capacity: 1, WaitForCapacity: true})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	if err := q.Submit(context.Background(), signal.Task{Shutdown: signal.Drain, Run: func(context.Context) error { close(started); <-release; return nil }}); err != nil {
		t.Fatal(err)
	}
	<-started
	records := make(chan slog.Record, 5)
	logs := agentLogger{runtime: slog.New(recordHandler{records: records, fail: true})}
	source := signal.New[agentevents.UserInputReceivedEvent]("input")
	_, err = source.Connect(logs.userInput, signal.ConnectOptions{Executor: q, Lifetime: signal.FollowExecutor, Shutdown: signal.Drain})
	if err != nil {
		t.Fatal(err)
	}
	meta := agentevents.EventMeta{At: time.Now(), SessionID: "s"}
	if err := source.Emit(context.Background(), agentevents.UserInputReceivedEvent{EventMeta: meta, Text: "B"}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- source.Emit(context.Background(), agentevents.UserInputReceivedEvent{EventMeta: meta, Text: "C"})
	}()
	select {
	case err := <-result:
		t.Fatalf("full log queue did not wait: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if len(records) != 0 {
		t.Fatal("producer bypassed consumer")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := q.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"B", "C"} {
		if attrs := recordAttrs(awaitRecord(t, records)); attrs["text"] != text {
			t.Fatal(attrs)
		}
	}
}

func TestRunnerStartsQueueShutdownBeforeWaitingForProducer(t *testing.T) {
	for _, producer := range []string{"platform", "cron"} {
		t.Run(producer, func(t *testing.T) {
			var events []string
			runner := newTestRunner(t, &events, RunModeFull, "")
			runner.shutdownTimeout = time.Second
			q, err := signal.NewQueue(signal.QueueOptions{Capacity: 1, WaitForCapacity: true})
			if err != nil {
				t.Fatal(err)
			}
			b := &signalBindings{queues: []*signal.Queue{q}}
			ready, release, started, exited := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			blockedProducer := func() error {
				defer close(exited)
				if err := q.Submit(context.Background(), signal.Task{Shutdown: signal.Drain, Run: func(context.Context) error { close(started); <-release; return nil }}); err != nil {
					return err
				}
				<-started
				if err := q.Submit(context.Background(), signal.Task{Shutdown: signal.Drain, Run: func(context.Context) error { return nil }}); err != nil {
					return err
				}
				close(ready)
				err := q.Submit(context.Background(), signal.Task{Shutdown: signal.Drain, Run: func(context.Context) error { t.Error("unaccepted log ran"); return nil }})
				close(release)
				if !errors.Is(err, signal.ErrClosed) {
					return errors.New("blocked admission was not closed")
				}
				return nil
			}
			base := runner.deps.Foundation
			runner.deps.Foundation = foundationFactoryFunc(func(ctx context.Context, req FoundationRequest) (*FoundationComponents, error) {
				f, err := base.Build(ctx, req)
				if producer == "cron" {
					f.StopCron = func(ctx context.Context) error {
						select {
						case <-exited:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}
				}
				return f, err
			})
			runner.deps.Runtime = runtimeFactoryFunc(func(context.Context, RuntimeRequest) (*RuntimeComponents, error) {
				return &RuntimeComponents{Handler: handlerStub{}, Signals: b, Lifecycle: lifecycleFunc(func(context.Context) error { return nil })}, nil
			})
			runner.deps.Executor = executorFunc(func(context.Context, PlatformRunRequest) error {
				if producer == "platform" {
					return blockedProducer()
				}
				go func() {
					if err := blockedProducer(); err != nil {
						t.Error(err)
					}
				}()
				<-ready
				return nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- runner.Run(ctx, Options{}) }()
			<-ready
			cancel()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown deadlocked on producer")
			}
			if !b.stopped() {
				t.Fatal("queue did not drain")
			}
			select {
			case <-exited:
			default:
				t.Fatal("producer never left admission")
			}
		})
	}
}
