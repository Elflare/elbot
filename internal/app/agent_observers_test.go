package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"elbot/internal/agent"
	"elbot/internal/llm"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func observerSignals() agent.Signals {
	return agent.Signals{
		UserInputReceived:   signal.New[agent.UserInputReceivedEvent]("input", nil),
		PersistenceFailed:   signal.New[agent.PersistenceFailedEvent]("persistence", nil),
		TurnTimedOut:        signal.New[agent.TurnTimedOutEvent]("timeout", nil),
		ModelCallCompleted:  signal.New[agent.ModelCallCompletedEvent]("model", nil),
		ToolCallCompleted:   signal.New[agent.ToolCallCompletedEvent]("tool", nil),
		ConfirmationChanged: signal.New[agent.ConfirmationChangedEvent]("confirmation", nil),
		ToolDenied:          signal.New[agent.ToolDeniedEvent]("denied", nil),
		StatusChanged:       signal.New[agent.StatusChangedEvent]("status", nil),
		VisionFallbackUsed:  signal.New[agent.VisionFallbackUsedEvent]("vision", nil),
		HookFailed:          signal.New[agent.HookFailedEvent]("hook", nil),
		ReplyDelivered:      signal.New[agent.ReplyDeliveredEvent]("delivery", nil),
		ReplyCommitted:      signal.New[agent.ReplyCommittedEvent]("commit", nil),
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Facts remain consumable after their request is finished.
	meta := agent.EventMeta{At: time.Unix(123, 0), SessionID: "s", RunID: "r", Attempt: "a"}
	model := agent.ModelCallCompletedEvent{EventMeta: meta, Provider: "p", Model: "m", OutputReady: true, Text: "rewritten", SourceText: "source", Usage: &llm.Usage{TotalTokens: 42}, ElapsedMS: 8}
	if err := events.ModelCallCompleted.Emit(ctx, model); err != nil {
		t.Fatal(err)
	}
	if err := events.ToolCallCompleted.Emit(ctx, agent.ToolCallCompletedEvent{EventMeta: meta, Arguments: " {\"x\": 1} ", Record: storage.ToolCallRecord{ToolName: "tool", ToolCallID: "id", ActorID: "actor", RiskLevel: "low", Success: true, ResultPreview: "done"}, RecordErr: errors.New("record failed")}); err != nil {
		t.Fatal(err)
	}
	if err := events.ConfirmationChanged.Emit(ctx, agent.ConfirmationChangedEvent{EventMeta: meta, Phase: "result", Action: "reject", Tool: "tool", Risk: "high", Reason: "no"}); err != nil {
		t.Fatal(err)
	}
	if err := events.ReplyCommitted.Emit(ctx, agent.ReplyCommittedEvent{EventMeta: meta, Persisted: true, MessageID: "msg", AssociationErrors: []error{agent.AssociationFailure{PlatformMessageID: "sent", Err: errors.New("mapping failed")}}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := awaitRecord(t, runtimeRecords)
	attrs := recordAttrs(first)
	if first.Message != "llm output" || !first.Time.Equal(meta.At) || attrs["text"] != "rewritten" || attrs["raw_text"] != "source" {
		t.Fatalf("model output: %+v %v", first, attrs)
	}
	if r := awaitRecord(t, runtimeRecords); r.Message != "record tool call failed" {
		t.Fatal(r)
	}
	tool := awaitRecord(t, runtimeRecords)
	if attrs := recordAttrs(tool); tool.Message != "tool call" || attrs["success"] != true || attrs["arguments"] != "{\"x\":1}" {
		t.Fatalf("record failure changed execution success: %v", attrs)
	}
	if r := awaitRecord(t, runtimeRecords); r.Message != "map platform message failed" {
		t.Fatal(r)
	}
	commit := awaitRecord(t, runtimeRecords)
	if recordAttrs(commit)["persisted"] != true {
		t.Fatal("association failure erased successful persistence")
	}
	for _, name := range []string{"llm_usage", "tool_call", "risk_confirmation_result", "persistence_error"} {
		r := awaitRecord(t, auditRecords)
		if recordAttrs(r)["event"] != name || !r.Time.Equal(meta.At) {
			t.Fatalf("audit order/fields: %v", recordAttrs(r))
		}
	}
	if len(runtimeRecords) != 0 || len(auditRecords) != 0 {
		t.Fatal("duplicate observations")
	}
}

func TestProductionAssemblyObservesConversationExactlyOnce(t *testing.T) {
	req, _, _ := runtimeAssemblyFixture(t)
	records := make(chan slog.Record, 512)
	logger := slog.New(recordHandler{records: records})
	req.Foundation.Logger = logger
	runtime, err := (defaultRuntimeFactory{}).Build(context.Background(), req)
	t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
	if err != nil {
		t.Fatal(err)
	}
	runtime.Agent.SetLogger(logger)
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

func TestLogBackpressureUsesConsumerAndReportsWriteFailure(t *testing.T) {
	diagnostics := make(chan slog.Record, 8)
	q, err := signal.NewQueue(signal.QueueOptions{Capacity: 1, WaitForCapacity: true, Logger: slog.New(recordHandler{records: diagnostics})})
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
	source := signal.New[agent.UserInputReceivedEvent]("input", nil)
	_, err = source.Connect(logs.userInput, signal.ConnectOptions{Executor: q, Lifetime: signal.FollowExecutor, Shutdown: signal.Drain})
	if err != nil {
		t.Fatal(err)
	}
	meta := agent.EventMeta{At: time.Now(), SessionID: "s"}
	if err := source.Emit(context.Background(), agent.UserInputReceivedEvent{EventMeta: meta, Text: "B"}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- source.Emit(context.Background(), agent.UserInputReceivedEvent{EventMeta: meta, Text: "C"})
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
		diagnostic := awaitRecord(t, diagnostics)
		if diagnostic.Message != "signal task failed" || !strings.Contains(recordAttrs(diagnostic)["error"].(error).Error(), "disk write failed") {
			t.Fatal(diagnostic)
		}
	}
}

func TestRunnerStartsQueueShutdownBeforeWaitingForProducer(t *testing.T) {
	for _, producer := range []string{"platform", "cron"} {
		t.Run(producer, func(t *testing.T) {
			var events []string
			runner := newTestRunner(t, &events, RunModeFull, "")
			runner.shutdownTimeout = time.Second
			q, err := signal.NewQueue(signal.QueueOptions{Capacity: 1, WaitForCapacity: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
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
