package execution

import (
	"context"
	"testing"

	agenthooks "elbot/internal/agent/hooks"
	agentoutput "elbot/internal/agent/output"
	agentshare "elbot/internal/agent/share"
	agenttools "elbot/internal/agent/tools"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

func TestHookAdaptersPassFailedResultWithNilBusinessError(t *testing.T) {
	declared := events.LogResult{ResultStatus: events.ResultFailed, Content: "source failure independent of business return"}
	manager := newTestHookManager()

	for _, point := range []hook.Point{hook.PointToolCallPrepared, hook.PointToolCallCompleted, hook.PointAgentTurnOutputPrepared} {
		manager.Register(hook.Registration{Name: string(point), Point: point, Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, events.LogResult, error) {
			return event, declared, nil
		})})
	}
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Manager: manager, Identity: agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{ScopeID: "test"})})
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
	text, result, err := output.PrepareAssistant(t.Context(), hook.PointAgentTurnOutputPrepared, "assistant business body")
	if err != nil || text != "assistant business body" || result != declared {
		t.Fatalf("text=%q result=%+v err=%v", text, result, err)
	}
}
