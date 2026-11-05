package dialogue

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/testutil"
	"elbot/internal/tool"
	"testing"

	"elbot/internal/agent/hooks"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

type hookInputCommitter struct {
	id    string
	text  string
	calls int
}

func (c *hookInputCommitter) Commit(_ context.Context, m *storage.Message, _ []storage.PlatformMessageMap) error {
	c.calls++
	c.text = m.Content
	c.id = m.ID
	return nil
}

func TestRequestPointOnlyBindsNewPendingAndKeepsHistoryDetached(t *testing.T) {
	for _, withPending := range []bool{false, true} {
		t.Run(map[bool]string{false: "without pending", true: "with pending"}[withPending], func(t *testing.T) {
			bridge := hooks.NewHookBridge(hooks.HookBridgeDeps{})
			defer bridge.Close(context.Background())
			var hookID string
			_, err := bridge.Register(hooks.Points{LLMRequestPrepared: func(_ context.Context, _ hooks.Facts, input agentshare.LLMInput) (agentshare.LLMInputResult, error) {
				if (input.Message.ID != "") != withPending {
					t.Fatalf("bound message=%+v", input.Message)
				}
				// A provider cannot change stored history through the input snapshot.
				hookID = input.Message.ID
				input.Messages[0].Segments = llm.TextSegments("attempted history edit")
				return agentshare.LLMInputResult{Segments: llm.TextSegments("edited pending"), Tools: input.Tools}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			messages := []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("initial")}}
			var pending *agentshare.PendingUserMessage
			if withPending {
				messages = append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("pending")})
				pending = &agentshare.PendingUserMessage{Message: storage.Message{Content: "pending"}, MessageIndex: 1}
			}
			commits := &hookInputCommitter{}
			processor := &CallProcessor{Hooks: bridge, Preparer: &Preparer{NewMessageID: testutil.IDSequence().MessageID}, Tools: &ToolExecutor{Manager: hookTools{}}}
			got, err := processor.PrepareCall(t.Context(), &storage.Session{ID: "s", Mode: storage.SessionModeWork}, modelmgr.Selection{}, messages, nil, pending, commits)
			if err != nil {
				t.Fatal(err)
			}
			if llm.SegmentsTextOnly(got.Messages[0].Segments) != "initial" || llm.SegmentsTextOnly(got.RequestMessages[0].Segments) != "initial" {
				t.Fatalf("history changed: %+v", got)
			}
			if withPending {
				if commits.id != hookID || pending.Message.ID != hookID {
					t.Fatal("pending identity changed after hook")
				}
				if commits.calls != 1 || commits.text != "edited pending" || llm.SegmentsTextOnly(got.RequestMessages[1].Segments) != "edited pending" {
					t.Fatalf("pending=%+v commits=%+v", got, commits)
				}
			} else if commits.calls != 0 || len(got.Messages) != 1 {
				t.Fatal("initial input rebound by request point")
			}
		})
	}
}

type hookTools struct{ *tool.Supervisor }

func (hookTools) DecodeState(string) (tool.State, error) { return tool.State{}, nil }
func (hookTools) FilterSchemas(_ context.Context, schemas []llm.ToolSchema) ([]llm.ToolSchema, error) {
	return schemas, nil
}
