package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
	"elbot/internal/storage"
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
	if records[0].Name != "assistant_message" || records[0].Detail == "" || records[1].Name != "llm_usage" || records[2].Name != "llm_error" || records[2].Detail != (testDiagnosticError{}).LogDiagnostic().Detail || records[2].Level != slog.LevelWarn {
		t.Fatalf("model facts: %+v", records[:3])
	}
	if records[3].Level != slog.LevelError || records[4].Name != "tool_call" || records[5].Level != slog.LevelInfo {
		t.Fatalf("persistence error changed tool success: %+v", records[3:])
	}
	_ = a.signals.ToolCallCompleted.Emit(ctx, agentevents.ToolCallCompletedEvent{EventMeta: meta, Record: storage.ToolCallRecord{ToolName: "tool", Error: "execution failed"}})
	if len(records) != 8 || records[6].Level != slog.LevelWarn || records[7].Level != slog.LevelWarn {
		t.Fatalf("tool failure severity: %+v", records[6:])
	}
	a.disconnectLogSignals()
	_ = a.signals.UserInputReceived.Emit(ctx, agentevents.UserInputReceivedEvent{Text: "after close"})
	if len(records) != 8 {
		t.Fatal("projection retained after close")
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
