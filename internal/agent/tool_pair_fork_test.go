package agent

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/tool"
)

func TestChatForkFromToolPairIncludesOnlyResultsAtBoundary(t *testing.T) {
	store := newTestStore(t)
	model := &fakeLLM{chunks: [][]chatcompletions.Chunk{
		{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{Index: 0, ID: "first", Name: "once", Args: `{}`}, {Index: 1, ID: "later", Name: "once", Args: `{"later":true}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "later source answer"}},
		{{DeltaContent: "branch answer"}},
	}}
	executions := 0
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "once", run: func(_ context.Context, request tool.CallRequest) (*tool.Result, error) {
		executions++
		if strings.Contains(string(request.Arguments), "later") {
			return &tool.Result{Content: "later source result"}, nil
		}
		return &tool.Result{Content: "result at fork"}, nil
	}})
	a := newTestAgent(t, &fakePlatform{}, model, "m", config.ProviderConfig{}, store, func(opts *testAgentOptions) { opts.ToolRegistry = registry })
	if err := a.HandleMessage(t.Context(), "@tool:once source"); err != nil {
		t.Fatal(err)
	}
	source, err := a.execution.sessions.Current(t.Context(), a.Scope(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.Messages().ListBySession(t.Context(), source.ID)
	if err != nil || len(rows) != 6 {
		t.Fatalf("source=%+v %v", rows, err)
	}
	if _, err := a.execution.sessions.Fork(t.Context(), a.Scope(t.Context()), rows[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := a.HandleMessage(t.Context(), "branch question"); err != nil {
		t.Fatal(err)
	}
	requests := model.chatRequests()
	if len(requests) != 3 || executions != 2 {
		t.Fatalf("requests=%d executions=%d", len(requests), executions)
	}
	calls, results := 0, 0
	for _, message := range requests[2].Messages {
		for _, call := range message.ToolCalls {
			calls++
			if call.ID != "first" {
				t.Fatalf("future call=%+v", call)
			}
		}
		if message.Role == llm.RoleTool {
			results++
			if message.ToolCallID != "first" || !strings.Contains(llm.SegmentsContentText(message.Segments), "result at fork") {
				t.Fatalf("wrong fork result=%+v", message)
			}
		}
		if strings.Contains(llm.SegmentsContentText(message.Segments), "later source") {
			t.Fatalf("future history=%+v", message)
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("calls/results=%d/%d", calls, results)
	}
}
