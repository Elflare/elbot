// 验证规则动作遵守工具调用只读策略。

package rules

import (
	"context"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
	"strings"
	"testing"
)

func TestReadOnlyRuleFailureRestoresPipelineInputAndStopsFollowingRule(t *testing.T) {
	manager := pipeline.NewManager(testLog, nil)
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}, Rules: []Rule{
		{Name: "rewrite", On: string(share.PointToolCallPrepared), Match: []share.Condition{{Op: share.MatchAlways}}, Actions: []Action{{Type: "append", Field: "tool.arguments", Text: "changed"}}},
		{Name: "following", On: string(share.PointToolCallPrepared), Match: []share.Condition{{Op: share.MatchAlways}}, Actions: []Action{{Type: "send", Text: "must not run"}}},
	}}
	module.RegisterHooks(manager)
	event := share.Event{Point: share.PointToolCallPrepared, Tool: share.ToolPayload{ID: "call", Name: "tool", Arguments: "{}"}}
	got, _, err := manager.Run(context.Background(), event, pipeline.CallOptions{Woken: true, ReadOnlyCalls: true})
	if err == nil || !strings.Contains(err.Error(), "read-only") || got.Tool.Arguments != "{}" || len(got.Outputs) != 0 {
		t.Fatalf("read-only failure escaped rule boundary: event=%+v err=%v", got, err)
	}
}

func TestReadOnlyArgumentsStopsRuleBeforeFollowingExecAction(t *testing.T) {
	rule := Rule{Actions: []Action{{Type: "append", Field: "tool.arguments", Text: "changed"}, {Type: "exec", Command: execHelperCommand("print", "should not run")}}}
	event := share.Event{Point: share.PointToolCallPrepared, Tool: share.ToolPayload{ID: "call", Name: "tool", Arguments: "{}"}}
	got, _, err := (Module{Opts: Options{Log: testLog, Notify: testNotify}}).runRule(t.Context(), rule, event, true)
	if err == nil || !strings.Contains(err.Error(), "read-only") || len(got.Outputs) != 0 {
		t.Fatalf("rule continued after rewrite: %+v %v", got, err)
	}
	got, _, err = (Module{Opts: Options{Log: testLog, Notify: testNotify}}).runRule(t.Context(), rule, event, false)
	if err != nil || len(got.Outputs) != 1 || got.Tool.Arguments != "{}changed" {
		t.Fatalf("ordinary rewrite failed: %+v %v", got, err)
	}
}

func TestReadOnlyCallsAllowsExecDisplayAndToolResults(t *testing.T) {
	for _, point := range []share.Point{share.PointLLMResponseReceived, share.PointToolCallCompleted} {
		event := share.Event{Point: point, Tool: share.ToolPayload{ID: "call", Name: "tool", Arguments: "{}"}}
		field := "tool.result"
		if point == share.PointLLMResponseReceived {
			field = "llm.text"
		}
		got, _, err := (Module{Opts: Options{Log: testLog, Notify: testNotify}}).runRule(t.Context(), Rule{Actions: []Action{{Type: "exec", Field: field, Command: execHelperCommand("done-message")}}}, event, true)
		if err != nil || validateCalls(true, event, got) != nil {
			t.Fatalf("exec policy=%+v %v", got, err)
		}
	}
}
