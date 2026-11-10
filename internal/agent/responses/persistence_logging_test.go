package responses

import (
	"context"
	"errors"
	"testing"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func TestNativeToolPreparationReturnsInternalFailure(t *testing.T) {
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
			head := agentshare.ToolCallStorageMessage("session", "", "", state.calls, "")
			if malformed {
				head.Metadata = "{"
			}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
				records = append(records, record)
				if err := errors.New("logger unavailable"); err != nil {
					t.Error(err)
				}

			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			err = committer.Begin(t.Context(), &head)
			if err == nil || (!malformed && err != failure) {
				t.Fatalf("business error changed: %v", err)
			}
			if len(records) != 0 {
				t.Fatalf("terminal failure reported below task boundary: %+v", records)
			}
		})
	}
}
