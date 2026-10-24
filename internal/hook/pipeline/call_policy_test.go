// 验证流水线中的只读调用约束与可编辑结果。

package pipeline

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"strings"
	"testing"

	"elbot/internal/llm"
)

func TestReadOnlyCallsRejectsInPlaceMutationBeforeNextHook(t *testing.T) {
	for _, point := range []share.Point{share.PointLLMResponseReceived, share.PointToolCallPrepared} {
		t.Run(string(point), func(t *testing.T) {
			manager := newTestManager()

			next := false
			manager.Register(share.Registration{Point: point, Name: "rewrite", Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, e share.Event) (share.Event, events.LogResult, error) {
				if point == share.PointLLMResponseReceived {
					e.LLM.ToolCalls[0].Arguments = `{"changed":true}`
				} else {
					e.Tool.Arguments = `{"changed":true}`
				}
				return e, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
			})})
			manager.Register(share.Registration{Point: point, Name: "next", Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, e share.Event) (share.Event, events.LogResult, error) {
				next = true
				return e, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
			})})
			event := share.Event{Point: point, LLM: share.LLMPayload{ToolCalls: []llm.ToolCallRequest{{ID: "call", Name: "tool", Arguments: `{}`}}}, Tool: share.ToolPayload{ID: "call", Name: "tool", Arguments: `{}`}}
			got, _, err := manager.Run(t.Context(), event, CallOptions{Woken: true, ReadOnlyCalls: true})
			if err == nil || !strings.Contains(err.Error(), "read-only") || next || got.LLM.ToolCalls[0].Arguments != "{}" || got.Tool.Arguments != "{}" {
				t.Fatalf("mutation accepted: %+v %v next=%v", got, err, next)
			}
		}, CallOptions{Woken: true})
	}
}

func TestReadOnlyCallsKeepsDisplayAndToolResultEditableAndOrdinaryCallsMutable(t *testing.T) {
	manager := newTestManager()

	manager.Register(share.Registration{Point: share.PointLLMResponseReceived, Name: "display", Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, e share.Event) (share.Event, events.LogResult, error) {
		e.LLM.Text = "visible"
		return e, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
	})})
	got, _, err := manager.Run(t.Context(), share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "original"}}, CallOptions{Woken: true, ReadOnlyCalls: true})
	if err != nil || got.LLM.Text != "visible" {
		t.Fatalf("display=%+v %v", got, err)
	}
	manager = newTestManager()

	manager.Register(share.Registration{Point: share.PointToolCallPrepared, Name: "arguments", Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, e share.Event) (share.Event, events.LogResult, error) {
		e.Tool.Arguments = `{"changed":true}`
		return e, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
	})})
	got, _, err = manager.Run(t.Context(), share.Event{Point: share.PointToolCallPrepared, Tool: share.ToolPayload{Arguments: `{}`}}, CallOptions{Woken: true})
	if err != nil || got.Tool.Arguments != `{"changed":true}` {
		t.Fatalf("ordinary rewrite=%+v %v", got, err)
	}
}
