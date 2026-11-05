package dialogue

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

func TestToolTranscriptPreparationOwnsInternalFailure(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "allocation", true: "metadata"}[malformed], func(t *testing.T) {
			failure := errors.New("message ID database unavailable")
			committer := &ToolTranscriptCommitter{Messages: &MessageStore{NewMessageID: func(context.Context) (string, error) { return "", failure }}}
			head := agentshare.ToolCallStorageMessage("session", "", "", []llm.ToolCallRequest{{ID: "call", Name: "tool"}})
			if malformed {
				head.Metadata = "{"
			}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				records = append(records, record)
				return errors.New("logger unavailable")
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			err = committer.Begin(t.Context(), &head)
			if err == nil || (!malformed && err != failure) {
				t.Fatalf("business error changed: %v", err)
			}
			if len(records) != 1 || records[0].Level != slog.LevelError || records[0].EventName != "tool_batch_prepare_failed" || !strings.Contains(records[0].Content, err.Error()) || records[0].Identity.SessionID != "session" {
				t.Fatalf("missing or duplicate internal diagnostic: %+v", records)
			}
		})
	}
}
