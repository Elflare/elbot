package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type confirmedBatchTool struct {
	name    string
	started chan string
	release chan struct{}
}

func (t confirmedBatchTool) Name() string { return t.name }
func (t confirmedBatchTool) Info() tool.Info {
	return tool.Info{Name: t.Name(), Source: tool.SourceBuiltin, Risk: tool.RiskHigh}
}
func (t confirmedBatchTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: t.Name(), Parameters: map[string]any{"type": "object"}}
}
func (t confirmedBatchTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, string, error) {
	t.started <- req.ID
	select {
	case <-t.release:
		return &tool.Result{Content: req.ID}, req.ID, nil
	case <-ctx.Done():
		return nil, "canceled", ctx.Err()
	}
}

func TestBatchToolAndSessionConfirmationKeepConcurrentCallsAndStableHistory(t *testing.T) {
	for _, command := range []string{"/confirmtool", "/confirmall"} {
		t.Run(command, func(t *testing.T) {
			other := "confirmed_batch"
			if command == "/confirmall" {
				other = "confirmed_other"
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p := &fakePlatform{}
			model := &fakeLLM{chunks: [][]chatcompletions.Chunk{
				{{DeltaContent: "starting", ToolCallDeltas: []chatcompletions.ToolCallDelta{
					{Index: 0, ID: "A", Name: "confirmed_batch", Args: "{}"},
					{Index: 1, ID: "B", Name: other, Args: "{}"},
					{Index: 2, ID: "C", Name: other, Args: "{}"},
				}, FinishReason: "tool_calls"}},
				{{DeltaContent: "done"}},
				{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{Index: 0, ID: "D", Name: other, Args: "{}"}}, FinishReason: "tool_calls"}},
				{{DeltaContent: "next turn"}},
			}}
			started, release := make(chan string, 3), make(chan struct{})
			registry := tool.NewRegistry()
			_ = registry.Register(confirmedBatchTool{name: "confirmed_batch", started: started, release: release})
			if other != "confirmed_batch" {
				_ = registry.Register(confirmedBatchTool{name: other, started: started, release: release})
			}
			store := newTestStore(t)
			a := newTestAgent(t, p, model, "m", config.ProviderConfig{}, store, func(opts *testAgentOptions) { opts.ToolRegistry = registry })
			row, err := a.execution.sessions.Create(ctx, a.Scope(ctx), session.CreateRequest{})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- a.HandleMessage(ctx, "@tool:confirmed_batch @tool:"+other+" run") }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				confirmation, ok := a.execution.turns.PendingRiskConfirmation(row.ID)
				if ok {
					if confirmation.ID != "A" {
						t.Fatalf("first confirmation=%+v", confirmation)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("confirmation was not registered")
				}
				time.Sleep(time.Millisecond)
			}
			if err := a.HandleMessage(ctx, command); err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for len(seen) < 3 {
				select {
				case id := <-started:
					seen[id] = true
				case <-time.After(3 * time.Second):
					t.Fatalf("same tool requested another confirmation or ran serially: %v", seen)
				}
			}
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("batch did not finish")
			}
			if strings.Count(p.out.String(), "高风险工具调用等待确认") != 1 {
				t.Fatalf("repeated confirmations: %s", p.out.String())
			}
			rows, err := store.Messages().ListBySession(ctx, row.ID)
			if err != nil || len(rows) != 6 {
				t.Fatalf("batch history=%+v err=%v", rows, err)
			}
			ids, err := storage.ToolResultMessageIDs(rows[1])
			if err != nil || len(ids) != 3 {
				t.Fatalf("batch links=%v err=%v", ids, err)
			}
			for i, id := range []string{"A", "B", "C"} {
				if rows[i+2].ToolCallID != id || rows[i+2].ID != ids[i] {
					t.Fatalf("result order=%+v", rows)
				}
			}
			if err := a.HandleMessage(ctx, "new turn"); err != nil {
				t.Fatal(err)
			}
			requests := model.chatRequests()
			for _, index := range []int{1, 2} {
				calls, results := 0, 0
				for _, message := range requests[index].Messages {
					if len(message.ToolCalls) > 0 {
						calls++
						if len(message.ToolCalls) != 3 || llm.SegmentsContentText(message.Segments) != "starting" {
							t.Fatalf("assistant batch rewritten: %+v", message)
						}
						for i, id := range []string{"A", "B", "C"} {
							if message.ToolCalls[i].ID != id {
								t.Fatalf("call order=%+v", message.ToolCalls)
							}
						}
					}
					if message.Role == llm.RoleTool {
						results++
					}
				}
				if calls != 1 || results != 3 {
					t.Fatalf("request %d has %d call messages and %d results", index, calls, results)
				}
			}
			if strings.Count(p.out.String(), "高风险工具调用等待确认") != 1 {
				t.Fatalf("confirmation state did not survive next batch: %s", p.out.String())
			}
		})
	}
}
