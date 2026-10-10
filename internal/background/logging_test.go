package background

import (
	"context"
	"log/slog"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestLogBackgroundPublishesSourceRecordAfterCancellation(t *testing.T) {
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Connect(func(ctx context.Context, record events.LogRecord) error {
		if ctx.Err() != nil {
			t.Fatalf("publication retained canceled context: %v", ctx.Err())
		}
		records = append(records, record)
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = contextinfo.WithExecution(ctx, contextinfo.Execution{RunID: "context-only-run"})
	sessionID := "fixed-session"
	record := events.LogRecord{Category: events.LogAudit, Level: slog.LevelInfo, EventName: "source-failed", Module: "cron", Type: events.ContentJSON, Content: `{"reason":"source failure"}`, Identity: events.LogIdentity{SessionID: &sessionID}, ResultStatus: events.ResultFailed}
	LogBackground(ctx, record)
	record.Category, record.EventName = events.LogElnis, "second-source-record"
	LogBackground(ctx, record)
	sessionID = "changed"
	if len(records) != 2 || records[0].Category != events.LogAudit || records[0].EventName != "source-failed" || records[1].Category != events.LogElnis {
		t.Fatalf("independent records changed: %#v", records)
	}
	for _, got := range records {
		if got.Level != slog.LevelInfo || got.ResultStatus != events.ResultFailed || got.Content != record.Content || got.Identity.SessionID == nil || *got.Identity.SessionID != "fixed-session" || got.Identity.RunID != nil {
			t.Fatalf("source fields changed: %#v", got)
		}
	}
}
