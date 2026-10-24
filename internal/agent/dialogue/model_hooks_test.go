package dialogue

import (
	"context"
	"testing"

	"elbot/internal/agent/hooks"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

type hookInputCommitter struct {
	text  string
	calls int
}

func (c *hookInputCommitter) Commit(_ context.Context, m *storage.Message, _ []storage.PlatformMessageMap) (events.LogResult, error) {
	c.calls++
	c.text = m.Content
	return events.LogResult{}, nil
}

func TestRequestPointOnlyBindsNewPendingAndKeepsHistoryDetached(t *testing.T) {
	for _, withPending := range []bool{false, true} {
		t.Run(map[bool]string{false: "without pending", true: "with pending"}[withPending], func(t *testing.T) {
			bridge := hooks.NewHookBridge(hooks.HookBridgeDeps{})
			defer bridge.Close(context.Background())
			_, err := bridge.Register(hooks.Points{LLMRequestPrepared: func(_ context.Context, _ hooks.Facts, input hooks.LLMInput) (hooks.LLMInputResult, events.LogResult, error) {
				if (input.Message.ID != "") != withPending {
					t.Fatalf("bound message=%+v", input.Message)
				}
				// A provider cannot change stored history through the input snapshot.
				input.Messages[0].Segments = llm.TextSegments("attempted history edit")
				return hooks.LLMInputResult{Segments: llm.TextSegments("edited pending"), Tools: input.Tools}, events.LogResult{}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			messages := []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("initial")}}
			var pending *PendingUserMessage
			if withPending {
				messages = append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("pending")})
				pending = &PendingUserMessage{Message: storage.Message{ID: "pending", Content: "pending"}, MessageIndex: 1}
			}
			commits := &hookInputCommitter{}
			processor := &CallProcessor{Hooks: bridge, Preparer: &Preparer{}}
			got, err := processor.PrepareCall(t.Context(), &storage.Session{ID: "s", Mode: storage.SessionModeWork}, modelmgr.Selection{}, messages, nil, pending, commits, &events.LogRecord{})
			if err != nil {
				t.Fatal(err)
			}
			if llm.SegmentsTextOnly(got.Messages[0].Segments) != "initial" || llm.SegmentsTextOnly(got.RequestMessages[0].Segments) != "initial" {
				t.Fatalf("history changed: %+v", got)
			}
			if withPending {
				if commits.calls != 1 || commits.text != "edited pending" || llm.SegmentsTextOnly(got.RequestMessages[1].Segments) != "edited pending" {
					t.Fatalf("pending=%+v commits=%+v", got, commits)
				}
			} else if commits.calls != 0 || len(got.Messages) != 1 {
				t.Fatal("initial input rebound by request point")
			}
		})
	}
}
