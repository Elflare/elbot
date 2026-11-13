package execution

import (
	"context"
	agenthooks "elbot/internal/agent/hooks"
	agentshare "elbot/internal/agent/share"
	agenttools "elbot/internal/agent/tools"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"testing"
)

func TestCompleteToolCallExposesNameAndAcceptsMultimodalSegments(t *testing.T) {
	bridge := newTestHookBridge(t, agenthooks.Points{
		ToolCallCompleted: func(_ context.Context, facts agenthooks.Facts, result agentshare.ToolResult) ([]llm.MessageSegment, error) {
			if facts.Session.ID != "s1" || result.Call.Name != "screenshot" || result.Call.ID != "call_1" {
				t.Fatalf("facts=%+v result=%+v", facts, result)
			}
			return append(result.Result.Data, llm.MessageSegment{Type: llm.SegmentImage, URL: "https://example.com/result.png"}), nil
		},
	})
	deps := agenttools.NewToolRunDeps(agenttools.ToolRunDeps{Hooks: bridge}).ForTurn(nil, "")
	segments, err := deps.CompleteToolCall(t.Context(), &storage.Session{ID: "s1"}, llm.ToolCallRequest{ID: "call_1", Name: "screenshot", Arguments: "{}"}, "low", operationresult.Result[[]llm.MessageSegment]{Status: operationresult.Success, Data: llm.TextSegments("done")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 || segments[1].Type != llm.SegmentImage {
		t.Fatalf("segments=%+v", segments)
	}
}

func TestPrepareToolCallOnlyAcceptsArguments(t *testing.T) {
	bridge := newTestHookBridge(t, agenthooks.Points{
		ToolCallPrepared: func(_ context.Context, _ agenthooks.Facts, call llm.ToolCallRequest) (string, error) {
			call.ID, call.Name = "changed-id", "changed-name"
			return "{ }", nil
		},
	})
	deps := agenttools.NewToolRunDeps(agenttools.ToolRunDeps{Hooks: bridge}).ForTurn(nil, "")
	call, err := deps.PrepareToolCall(t.Context(), &storage.Session{ID: "s1"}, llm.ToolCallRequest{ID: "call_1", Name: "search", Arguments: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if call.ID != "call_1" || call.Name != "search" || call.Arguments != "{ }" {
		t.Fatalf("call=%+v", call)
	}
}
