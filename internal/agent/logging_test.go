package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/logging"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
)

type cancelLoggingLLM struct {
	fakeLLM
	cancel context.CancelFunc
}

func (c *cancelLoggingLLM) Stream(context.Context, chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	ch := make(chan chatcompletions.Chunk, 1)
	ch <- chatcompletions.Chunk{DeltaContent: "partial-before-cancel"}
	c.cancel()
	close(ch)
	return ch, nil
}

func TestChatSilentCancellationPreservesPartialOutputLog(t *testing.T) {
	client := &cancelLoggingLLM{}
	a := newTestAgent(t, &fakePlatform{}, client, "model", config.ProviderConfig{}, newTestStore(t))
	ctx, row, err := a.execution.ResolveInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	ctx, client.cancel = context.WithCancel(ctx)
	defer client.cancel()
	var records []globalevents.LogRecord
	connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, r globalevents.LogRecord) error {
		if r.EventName == "llm_usage" || r.EventName == "llm_error" || r.EventName == "assistant_message" {
			records = append(records, r)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	out := foregroundTurnOutput{sender: a.output}
	result, err := testChatRoute(a).Caller.Call(ctx, row, modelmgr.SelectionForTurn(ctx, a.execution.models, row), nil, nil, nil, nil, out)
	if err != nil || result.Text != "partial-before-cancel" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	if len(records) != 3 || records[0].EventName != "llm_usage" {
		t.Fatalf("records = %#v", records)
	}
	for _, r := range records {
		if r.ResultStatus != globalevents.ResultCanceled || r.Level != slog.LevelInfo || r.Identity.SessionID == nil || *r.Identity.SessionID != row.ID {
			t.Fatalf("record = %#v", r)
		}
	}
	if !strings.Contains(records[2].Content, "partial-before-cancel") {
		t.Fatal("partial output missing from log")
	}
	var content map[string]json.RawMessage
	if err := json.Unmarshal([]byte(records[0].Content), &content); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "cache_hit_tokens"} {
		if string(content[key]) != "null" {
			t.Fatalf("missing usage: %s = %s", key, content[key])
		}
	}
}

func TestReplyLogPreservesCanceledSendAndIndependentSaveFailure(t *testing.T) {
	f := newReplyTestFixture(t, false)
	f.platform.sendErr = context.Canceled
	f.repo.appendErr = errors.New("independent save failure")
	var records []globalevents.LogRecord
	connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, r globalevents.LogRecord) error {
		if r.EventName == "reply_delivered" || r.EventName == "persistence_error" {
			records = append(records, r)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	result, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
	if !errors.Is(err, f.repo.appendErr) || !errors.Is(result.SendErr, context.Canceled) || len(result.Receipt.SentMessages) != 1 {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	if len(records) != 2 || records[0].ResultStatus != globalevents.ResultCanceled || records[0].Level != slog.LevelInfo || records[1].ResultStatus != globalevents.ResultFailed || records[1].Level != slog.LevelError {
		t.Fatalf("records = %#v", records)
	}
	for _, fact := range []string{"context canceled", "platform_message_id=sent"} {
		if !strings.Contains(records[0].Content, fact) {
			t.Errorf("missing %q: %s", fact, records[0].Content)
		}
	}
	if !strings.Contains(records[1].Content, "independent save failure") {
		t.Fatalf("save diagnostic missing: %s", records[1].Content)
	}
}

func TestResponsesSuccessfulCallWithSaveFailureKeepsSourceFacts(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
		emitNative(w, "response", "completed", nativeText("upstream-success"))
	}, func(opts *testAgentOptions) {
		opts.Store = nativeTerminalFailureStore{Store: opts.Store, fail: &fail}
	})
	var event agentevents.ModelCallCompletedEvent
	connection, err := f.agent.Signals().ModelCallCompleted.Observe(func(_ context.Context, e agentevents.ModelCallCompletedEvent) error { event = e; return nil }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	var records []globalevents.LogRecord
	logs, err := globalevents.LogSubmitted.Observe(func(_ context.Context, r globalevents.LogRecord) error {
		if r.EventName == "llm_usage" {
			records = append(records, r)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Disconnect()
	if err := f.agent.HandleMessage(t.Context(), "input"); err == nil || !strings.Contains(err.Error(), "terminal audit storage failed") {
		t.Fatalf("error = %v", err)
	}
	if event.ProviderError || !event.OutputReady || event.Text != "upstream-success" {
		t.Fatalf("business event = %+v", event)
	}
	if len(records) != 1 || records[0].ResultStatus != globalevents.ResultFailed || !strings.Contains(records[0].Content, "persistence_error=terminal audit storage failed") {
		t.Fatalf("logs = %#v", records)
	}
}

func TestAgentLogSourcePreservesExplicitIdentity(t *testing.T) {
	var records []globalevents.LogRecord
	connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
		if record.EventName == "identity_test" {
			records = append(records, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "wrong", RequestID: "wrong"})
	emitAgentLog(ctx, agentevents.EventMeta{SessionID: "source"}, globalevents.LogRecord{
		Category:  globalevents.LogRuntime,
		Level:     slog.LevelInfo,
		EventName: "identity_test",
		Module:    "agent",
		Type:      globalevents.ContentText,
		Content:   "hello",
	})
	if len(records) != 1 || records[0].Identity.SessionID == nil || *records[0].Identity.SessionID != "source" || records[0].Identity.RequestID != nil {
		t.Fatalf("records = %#v", records)
	}
}

func TestModelObservationPreservesExplicitResultAndUsage(t *testing.T) {
	var records []globalevents.LogRecord
	connection, _ := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
		records = append(records, record)
		return nil
	}, signal.ConnectOptions{})
	defer connection.Disconnect()
	processor := &dialogue.CallProcessor{}
	observation := processor.Begin("session", modelmgr.Selection{ModelSelection: config.ModelSelection{Provider: "provider", Model: "model"}})
	tokens := int64(9007199254740993)
	observation.Event.Usage = &llm.Usage{TotalTokens: &tokens}
	observation.Event.OutputReady, observation.Event.Text = true, "partial output"
	observation.LogRecord.ResultStatus, observation.LogRecord.Level = globalevents.ResultFailed, slog.LevelError
	observation.LogRecord.Content = "call_error=context canceled\npersistence_error=disk full"
	observation.Publish(context.Background(), errors.Join(context.Canceled, errors.New("disk full")))
	observation.Publish(context.Background(), nil)
	if len(records) != 2 || records[0].ResultStatus != globalevents.ResultFailed || records[1].Content == "" {
		t.Fatalf("records = %#v", records)
	}
	tokens = 0
	if observation.Event.Usage.TotalTokens == nil || *observation.Event.Usage.TotalTokens != 9007199254740993 {
		t.Fatal("usage pointer was not copied")
	}
}

func TestStableLogNamesIndependentOfContent(t *testing.T) {
	center, err := logging.NewManager("info", filepath.Join(t.TempDir(), "db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = center.Close(context.Background()) })
	for _, content := range []string{"before wording", "after wording：变化"} {
		emitAgentLog(context.Background(), agentevents.EventMeta{}, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelInfo, EventName: "stable_event", Module: "agent", Content: content})
	}
	if err := center.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := (logging.Reader{Dir: center.LogDir()}).Query(context.Background(), logging.LogQuery{Prefix: "elbot", Fields: map[string]string{"event": "stable_event"}})
	if err != nil || len(result.Entries) != 2 || result.Entries[0].Content == result.Entries[1].Content {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestTurnTimeoutSeverity(t *testing.T) {
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
	var records []globalevents.LogRecord
	connection, _ := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
		if record.EventName == "turn_response_timeout" && record.Category == globalevents.LogAudit {
			records = append(records, record)
		}
		return nil
	}, signal.ConnectOptions{})
	defer connection.Disconnect()
	a.execution.handleTurnContextDone(context.Background(), "timeout-session", context.DeadlineExceeded, foregroundTurnOutput{sender: a.output})
	if len(records) != 1 || records[0].Level != slog.LevelError || records[0].ResultStatus != globalevents.ResultFailed {
		t.Fatalf("records = %#v", records)
	}
}

// captureLogs observes the public signal without injecting a production logger.
func captureLogs(t *testing.T, logger *slog.Logger) {
	t.Helper()
	connection, err := globalevents.LogSubmitted.Observe(func(ctx context.Context, record globalevents.LogRecord) error {
		if !logger.Enabled(ctx, record.Level) {
			return nil
		}
		out := slog.NewRecord(time.Now(), record.Level, record.Content, 0)
		out.AddAttrs(slog.String("event", record.EventName), slog.String("module", record.Module), slog.String("result_status", record.ResultStatus.String()))
		for key, value := range map[string]*string{
			"session_id":      record.Identity.SessionID,
			"run_id":          record.Identity.RunID,
			"attempt":         record.Identity.Attempt,
			"request_id":      record.Identity.RequestID,
			"root_request_id": record.Identity.RootRequestID,
		} {
			if value != nil {
				out.AddAttrs(slog.String(key, *value))
			}
		}
		return logger.Handler().Handle(ctx, out)
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
}
