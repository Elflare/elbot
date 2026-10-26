package execution

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"testing"

	agenthooks "elbot/internal/agent/hooks"
	agentoutput "elbot/internal/agent/output"
	agenttools "elbot/internal/agent/tools"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

func TestHookAdaptersPassFailedResultWithNilBusinessError(t *testing.T) {
	declared := events.LogResult{ResultStatus: events.ResultFailed, Content: "source failure independent of business return"}
	bridge := newTestHookBridge(t, agenthooks.Points{
		ToolCallPrepared: func(_ context.Context, _ agenthooks.Facts, call llm.ToolCallRequest) (string, events.LogResult, error) {
			return call.Arguments, declared, nil
		},
		ToolCallCompleted: func(_ context.Context, _ agenthooks.Facts, result agentshare.ToolResult) ([]llm.MessageSegment, events.LogResult, error) {
			return result.Segments, declared, nil
		},
		AgentTurnOutputPrepared: func(_ context.Context, _ agenthooks.Facts, text string) (string, events.LogResult, error) {
			return text, declared, nil
		},
	})
	deps := agenttools.NewToolRunDeps(agenttools.ToolRunDeps{Hooks: bridge}).ForTurn(nil, "")
	session := &storage.Session{ID: "session"}
	call := llm.ToolCallRequest{ID: "call", Name: "shell", Arguments: `{}`}
	prepared, result, err := deps.PrepareToolCall(t.Context(), session, call)
	if err != nil || prepared != call || result != declared {
		t.Fatalf("prepared=%+v result=%+v err=%v", prepared, result, err)
	}
	segments, result, err := deps.CompleteToolCall(t.Context(), session, call, "low", llm.TextSegments("business body"), nil)
	if err != nil || llm.SegmentsTextOnly(segments) != "business body" || result != declared {
		t.Fatalf("segments=%+v result=%+v err=%v", segments, result, err)
	}
	output := agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{Hooks: bridge})
	text, result, err := output.PrepareAssistant(t.Context(), "assistant business body")
	if err != nil || text != "assistant business body" || result != declared {
		t.Fatalf("text=%q result=%+v err=%v", text, result, err)
	}
}
