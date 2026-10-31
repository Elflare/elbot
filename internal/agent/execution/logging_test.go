package execution

import (
	"context"

	"errors"
	"log/slog"

	"path/filepath"
	"strings"

	"testing"
	"time"

	"elbot/internal/agent/dialogue"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"

	"elbot/internal/logging"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
)

func TestReplyLogPreservesCanceledSendAndIndependentSaveFailure(t *testing.T) {
	f := newReplyTestFixture(t, false)
	f.platform.sendErr = context.Canceled
	f.repo.appendErr = errors.New("independent save failure")
	var records []globalevents.LogRecord
	connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, r globalevents.LogRecord) error {
		if r.EventName == "reply_delivered" {
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
	if len(records) != 1 || records[0].ResultStatus != globalevents.ResultCanceled || records[0].Level != slog.LevelInfo {
		t.Fatalf("records = %#v", records)
	}
	for _, fact := range []string{"context canceled", `"platform_message_id":"sent"`} {
		if !strings.Contains(records[0].Content, fact) {
			t.Errorf("missing %q: %s", fact, records[0].Content)
		}
	}
	if !strings.Contains(err.Error(), "independent save failure") {
		t.Fatalf("save diagnostic missing from returned error: %v", err)
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
	agentshare.EmitAgentLog(ctx, contextinfo.Execution{SessionID: "source"}, globalevents.LogRecord{
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
	observation.Event().Usage = &llm.Usage{TotalTokens: &tokens}
	observation.Event().OutputReady, observation.Event().Text = true, "partial output"
	observation.LogRecord().ResultStatus, observation.LogRecord().Level = globalevents.ResultFailed, slog.LevelError
	observation.LogRecord().EventName = "llm_usage"
	observation.LogRecord().Content = "call_error=context canceled\npersistence_error=disk full"
	observation.Publish(context.Background(), errors.Join(context.Canceled, errors.New("disk full")))
	observation.Publish(context.Background(), nil)
	if len(records) != 2 || records[0].ResultStatus != globalevents.ResultFailed || records[1].Content == "" {
		t.Fatalf("records = %#v", records)
	}
	tokens = 0
	if observation.Event().Usage.TotalTokens == nil || *observation.Event().Usage.TotalTokens != 9007199254740993 {
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
		agentshare.EmitAgentLog(context.Background(), contextinfo.Execution{}, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelInfo, EventName: "stable_event", Module: "agent", Content: content})
	}
	if err := center.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := (logging.Reader{Dir: center.LogDir()}).Query(context.Background(), logging.LogQuery{Prefix: "elbot", Fields: map[string]string{"event": "stable_event"}})
	if err != nil || len(result.Entries) != 2 || result.Entries[0].Content == result.Entries[1].Content {
		t.Fatalf("result = %#v, err = %v", result, err)
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
