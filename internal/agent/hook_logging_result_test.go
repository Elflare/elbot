package agent

import (
	"context"
	"testing"

	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

func TestHookAdaptersPassFailedResultWithNilBusinessError(t *testing.T) {
	declared := events.LogResult{ResultStatus: events.ResultFailed, Content: "source failure independent of business return"}
	manager := hook.NewManager()
	for _, point := range []hook.Point{hook.PointToolCallPrepared, hook.PointToolCallCompleted, hook.PointAgentTurnOutputPrepared} {
		if err := manager.Register(hook.Registration{Name: string(point), Point: point, Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, events.LogResult, error) {
			return event, declared, nil
		})}); err != nil {
			t.Fatal(err)
		}
	}
	bridge := &hookBridge{manager: manager, identity: &identityResolver{scopeID: "test"}}
	deps := toolRunDeps{hooks: bridge}
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
	output := &outputSender{hooks: bridge}
	text, result, err := output.PrepareAssistant(t.Context(), hook.PointAgentTurnOutputPrepared, "assistant business body")
	if err != nil || text != "assistant business body" || result != declared {
		t.Fatalf("text=%q result=%+v err=%v", text, result, err)
	}
}
