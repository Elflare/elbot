package execution

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"testing"

	agenthooks "elbot/internal/agent/hooks"
	agentoutput "elbot/internal/agent/output"
	agenttools "elbot/internal/agent/tools"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
)

func TestHookAdaptersPreserveBusinessResults(t *testing.T) {
	bridge := newTestHookBridge(t, agenthooks.Points{
		ToolCallPrepared: func(_ context.Context, _ agenthooks.Facts, call llm.ToolCallRequest) (string, error) {
			return call.Arguments, nil
		},
		ToolCallCompleted: func(_ context.Context, _ agenthooks.Facts, result agentshare.ToolResult) ([]llm.MessageSegment, error) {
			if result.Result.Status != operationresult.Success {
				t.Fatalf("tool result: %+v", result.Result)
			}
			return result.Result.Data, nil
		},
		AgentTurnOutputPrepared: func(_ context.Context, _ agenthooks.Facts, text string) (string, error) {
			return text, nil
		},
	})
	deps := agenttools.NewToolRunDeps(agenttools.ToolRunDeps{Hooks: bridge}).ForTurn(nil, "")
	session := &storage.Session{ID: "session"}
	call := llm.ToolCallRequest{ID: "call", Name: "shell", Arguments: `{}`}
	prepared, err := deps.PrepareToolCall(t.Context(), session, call)
	if err != nil || prepared != call {
		t.Fatal("prepared= result= err=", prepared, err)
	}
	segments, err := deps.CompleteToolCall(t.Context(), session, call, "low", operationresult.Result[[]llm.MessageSegment]{Status: operationresult.Success, Data: llm.TextSegments("business body")}, nil)
	if err != nil || llm.SegmentsTextOnly(segments) != "business body" {
		t.Fatal("segments= result= err=", segments, err)
	}
	output := agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{Hooks: bridge})
	text, err := output.PrepareAssistant(t.Context(), "assistant business body")
	if err != nil || text != "assistant business body" {
		t.Fatal("text= result= err=", text, err)
	}
}
