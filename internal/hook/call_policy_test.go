package hook

import (
	"context"
	"elbot/internal/events"
	"strings"
	"testing"

	"elbot/internal/llm"
)

func TestReadOnlyCallsRejectsInPlaceMutationBeforeNextHook(t *testing.T) {
	for _, point := range []Point{PointLLMResponseReceived, PointToolCallPrepared} {
		t.Run(string(point), func(t *testing.T) {
			manager := newTestManager()

			next := false
			manager.Register(Registration{Point: point, Name: "rewrite", Match: Always(), Handler: HandlerFunc(func(_ context.Context, e Event) (Event, events.LogResult, error) {
				if point == PointLLMResponseReceived {
					e.LLM.ToolCalls[0].Arguments = `{"changed":true}`
				} else {
					e.Tool.Arguments = `{"changed":true}`
				}
				return e, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
			})})
			manager.Register(Registration{Point: point, Name: "next", Match: Always(), Handler: HandlerFunc(func(_ context.Context, e Event) (Event, events.LogResult, error) {
				next = true
				return e, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
			})})
			event := Event{Point: point, LLM: LLMPayload{ToolCalls: []llm.ToolCallRequest{{ID: "call", Name: "tool", Arguments: `{}`}}}, Tool: ToolPayload{ID: "call", Name: "tool", Arguments: `{}`}}
			got, _, err := manager.Run(WithReadOnlyCalls(t.Context()), event)
			if err == nil || !strings.Contains(err.Error(), "read-only") || next || got.LLM.ToolCalls[0].Arguments != "{}" || got.Tool.Arguments != "{}" {
				t.Fatalf("mutation accepted: %+v %v next=%v", got, err, next)
			}
		})
	}
}

func TestReadOnlyCallsKeepsDisplayAndToolResultEditableAndOrdinaryCallsMutable(t *testing.T) {
	manager := newTestManager()

	manager.Register(Registration{Point: PointLLMResponseReceived, Name: "display", Match: Always(), Handler: HandlerFunc(func(_ context.Context, e Event) (Event, events.LogResult, error) {
		e.LLM.Text = "visible"
		return e, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
	})})
	got, _, err := manager.Run(WithReadOnlyCalls(t.Context()), Event{Point: PointLLMResponseReceived, LLM: LLMPayload{Text: "original"}})
	if err != nil || got.LLM.Text != "visible" {
		t.Fatalf("display=%+v %v", got, err)
	}
	manager = newTestManager()

	manager.Register(Registration{Point: PointToolCallPrepared, Name: "arguments", Match: Always(), Handler: HandlerFunc(func(_ context.Context, e Event) (Event, events.LogResult, error) {
		e.Tool.Arguments = `{"changed":true}`
		return e, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
	})})
	got, _, err = manager.Run(t.Context(), Event{Point: PointToolCallPrepared, Tool: ToolPayload{Arguments: `{}`}})
	if err != nil || got.Tool.Arguments != `{"changed":true}` {
		t.Fatalf("ordinary rewrite=%+v %v", got, err)
	}
}
