package responses

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func TestNativeToolPreparationOwnsInternalFailure(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "allocation", true: "metadata"}[malformed], func(t *testing.T) {
			failure := errors.New("message ID database unavailable")
			state := &turnState{
				route: &Loop{LoopDeps: LoopDeps{NewMessageID: func(context.Context) (string, error) {
					if malformed {
						return "message", nil
					}
					return "", failure
				}}},
				session:  &storage.Session{ID: "session"},
				exchange: &storage.NativeExchange{ID: "exchange"},
				response: &api.Response{ID: "response"},
				calls:    []llm.ToolCallRequest{{ID: "call", Name: "tool"}},
			}
			committer := &toolCommitter{state: state}
			head := agentshare.ToolCallStorageMessage("session", "", "", state.calls)
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
