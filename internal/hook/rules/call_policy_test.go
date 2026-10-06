package rules

import (
	"elbot/internal/hook"
	"strings"
	"testing"
)

func TestReadOnlyArgumentsStopsRuleBeforeFollowingExecAction(t *testing.T) {
	rule := Rule{Actions: []Action{{Type: "append", Field: "tool.arguments", Text: "changed"}, {Type: "exec", Command: execHelperCommand("print", "should not run")}}}
	event := hook.Event{Point: hook.PointToolCallPrepared, Tool: hook.ToolPayload{ID: "call", Name: "tool", Arguments: "{}"}}
	got, err := (Module{}).runRule(hook.WithReadOnlyCalls(t.Context()), rule, event)
	if err == nil || !strings.Contains(err.Error(), "read-only") || len(got.Outputs) != 0 {
		t.Fatalf("rule continued after rewrite: %+v %v", got, err)
	}
	got, err = (Module{}).runRule(t.Context(), rule, event)
	if err != nil || len(got.Outputs) != 1 || got.Tool.Arguments != "{}changed" {
		t.Fatalf("ordinary rewrite failed: %+v %v", got, err)
	}
}

func TestReadOnlyCallsAllowsExecDisplayAndToolResults(t *testing.T) {
	for _, point := range []hook.Point{hook.PointLLMResponseReceived, hook.PointToolCallCompleted} {
		event := hook.Event{Point: point, Tool: hook.ToolPayload{ID: "call", Name: "tool", Arguments: "{}"}}
		field := "tool.result"
		if point == hook.PointLLMResponseReceived {
			field = "llm.text"
		}
		got, err := (Module{}).runRule(hook.WithReadOnlyCalls(t.Context()), Rule{Actions: []Action{{Type: "exec", Field: field, Command: execHelperCommand("done-message")}}}, event)
		if err != nil || hook.ValidateCalls(hook.WithReadOnlyCalls(t.Context()), event, got) != nil {
			t.Fatalf("exec policy=%+v %v", got, err)
		}
	}
}
