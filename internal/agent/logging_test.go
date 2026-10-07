package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

type testDiagnosticError struct{}

func (testDiagnosticError) Error() string { return "upstream failed" }
func (testDiagnosticError) LogDiagnostic() globalevents.LogDiagnostic {
	return globalevents.LogDiagnostic{Kind: "error", Detail: `{"description":"gateway failure"}`}
}

func TestAgentLogProjectionPreservesFactsAndDiagnostic(t *testing.T) {
	records := []globalevents.LogRecord{}
	connection, _ := globalevents.LogSubmitted.Connect(func(_ context.Context, r globalevents.LogRecord) error { records = append(records, r); return nil }, signal.ConnectOptions{})
	defer connection.Disconnect()
	a := &Agent{signals: agentevents.NewSignals()}
	if err := a.connectLogSignals(); err != nil {
		t.Fatal(err)
	}
	defer a.disconnectLogSignals()
	ctx, cancel := context.WithCancel(contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "wrong", RequestID: "wrong", RunID: "wrong", Attempt: "wrong", RootRequestID: "wrong"}))
	cancel()
	meta := agentevents.EventMeta{At: time.Unix(123, 0), SessionID: "s"}
	_ = a.signals.ModelCallCompleted.Emit(ctx, agentevents.ModelCallCompletedEvent{EventMeta: meta, Provider: "p", Model: "m", OutputReady: true, Text: "rewritten", SourceText: "source", Usage: &llm.Usage{TotalTokens: 42}})
	_ = a.signals.ModelCallCompleted.Emit(ctx, agentevents.ModelCallCompletedEvent{EventMeta: meta, ProviderError: true, Err: fmt.Errorf("wrapped: %w", testDiagnosticError{})})
	_ = a.signals.ToolCallCompleted.Emit(ctx, agentevents.ToolCallCompletedEvent{EventMeta: meta, Record: storage.ToolCallRecord{Success: true, ToolName: "tool"}, RecordErr: errors.New("save failed")})
	if len(records) != 6 {
		t.Fatalf("records: %+v", records)
	}
	for _, r := range records {
		if !r.At.Equal(meta.At) {
			t.Fatal("event time changed")
		}
		for _, field := range r.Fields {
			if field.Value.String() == "wrong" {
				t.Fatal("identity filled from consumer context")
			}
		}
	}
	if records[0].Name != "assistant_message" || records[0].Detail == "" || records[1].Name != "llm_usage" || records[2].Name != "llm_error" || records[2].Detail != (testDiagnosticError{}).LogDiagnostic().Detail || records[2].Level != slog.LevelError {
		t.Fatalf("model facts: %+v", records[:3])
	}
	if records[3].Level != slog.LevelError || records[4].Name != "tool_call" || records[5].Level != slog.LevelInfo {
		t.Fatalf("persistence error changed tool success: %+v", records[3:])
	}
	_ = a.signals.ToolCallCompleted.Emit(ctx, agentevents.ToolCallCompletedEvent{EventMeta: meta, Record: storage.ToolCallRecord{ToolName: "tool", Error: "execution failed"}})
	if len(records) != 8 || records[6].Level != slog.LevelError || records[7].Level != slog.LevelError {
		t.Fatalf("tool failure severity: %+v", records[6:])
	}
	a.disconnectLogSignals()
	_ = a.signals.UserInputReceived.Emit(ctx, agentevents.UserInputReceivedEvent{Text: "after close"})
	if len(records) != 8 {
		t.Fatal("projection retained after close")
	}
}

func TestPersistenceCancellationAndTurnTimeoutSeverity(t *testing.T) {
	var records []globalevents.LogRecord
	connection, err := globalevents.LogSubmitted.Connect(func(_ context.Context, record globalevents.LogRecord) error {
		records = append(records, record)
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	logs := agentLogProjection{}
	_ = logs.persistenceAuditLog(context.Background(), agentevents.PersistenceFailedEvent{Err: fmt.Errorf("wrapped: %w", context.Canceled)})
	_ = logs.persistenceAuditLog(context.Background(), agentevents.PersistenceFailedEvent{Err: errors.New("disk failed")})
	_ = logs.timeoutAuditLog(context.Background(), agentevents.TurnTimedOutEvent{Err: context.DeadlineExceeded})
	if len(records) != 3 || records[0].Level != slog.LevelInfo || records[1].Level != slog.LevelError || records[2].Level != slog.LevelError {
		t.Fatalf("records: %+v", records)
	}
}

func TestToolOutcomeSeverityAndStableNames(t *testing.T) {
	center, err := logging.NewManager("info", filepath.Join(t.TempDir(), "db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = center.Close(context.Background()) })
	a := &Agent{signals: agentevents.NewSignals()}
	if err := a.connectLogSignals(); err != nil {
		t.Fatal(err)
	}
	defer a.disconnectLogSignals()
	deps := toolRunDeps{completed: a.signals.ToolCallCompleted, identity: &identityResolver{}}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"success", nil}, {"failure", errors.New("broken")},
		{"canceled", fmt.Errorf("wrapped: %w", context.Canceled)},
		{"timeout", context.DeadlineExceeded}, {"denied", toolrun.PolicyDeniedError("arbitrary reason")},
	} {
		deps.RecordToolCall(context.Background(), "session", llm.ToolCallRequest{ID: tc.name, Name: tc.name}, "low", time.Now(), "result", tc.err)
	}
	for _, summary := range []string{"before wording", "after\twording：变化"} {
		emitAgentLog(context.Background(), globalevents.LogRuntime, agentevents.EventMeta{}, slog.LevelInfo, "stable_event", summary)
	}
	if err := center.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := (logging.Reader{Dir: center.LogDir()}).Query(context.Background(), logging.LogQuery{Prefix: "audit", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"success": "INFO", "failure": "ERROR", "canceled": "INFO", "timeout": "ERROR", "denied": "WARN"}
	if len(entries) != len(want) {
		t.Fatalf("got %d tool records", len(entries))
	}
	for _, entry := range entries {
		if entry.Fields["event"] != "tool_call" || entry.Level != want[entry.Fields["tool"]] {
			t.Fatalf("wrong outcome: %+v", entry)
		}
		if strings.Contains(entry.Raw, "!BADKEY") {
			t.Fatal(entry.Raw)
		}
	}
	entries, err = (logging.Reader{Dir: center.LogDir()}).Query(context.Background(), logging.LogQuery{Prefix: "elbot", Limit: 20, Fields: map[string]string{"event": "stable_event"}})
	if err != nil || len(entries) != 2 || entries[0].Message == entries[1].Message {
		t.Fatalf("event coupled to wording: %+v %v", entries, err)
	}
}

// captureLogs observes the public signal; production never receives this logger.
func captureLogs(t *testing.T, logger *slog.Logger) {
	t.Helper()
	connection, err := globalevents.LogSubmitted.Connect(func(ctx context.Context, record globalevents.LogRecord) error {
		if !logger.Enabled(ctx, record.Level) {
			return nil
		}
		out := slog.NewRecord(record.At, record.Level, record.Summary, 0)
		out.AddAttrs(record.Fields...)
		out.AddAttrs(slog.String("event", record.Name), slog.String("module", record.Module))
		if record.Detail != "" && logger.Enabled(ctx, slog.LevelDebug) {
			out.AddAttrs(slog.String("detail", record.Detail))
		}
		return logger.Handler().Handle(ctx, out)
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
}
