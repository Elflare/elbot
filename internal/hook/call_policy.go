package hook

import (
	"context"
	"fmt"
	"reflect"

	"elbot/internal/llm"
)

type readOnlyCallsKey struct{}

// WithReadOnlyCalls is a consumer policy, not a protocol decision in Hook.
func WithReadOnlyCalls(ctx context.Context) context.Context {
	return context.WithValue(ctx, readOnlyCallsKey{}, true)
}

// SnapshotCalls breaks slice aliases before handing a read-only view to a Hook.
func SnapshotCalls(event Event) Event {
	event.LLM.ToolCalls = append([]llm.ToolCallRequest(nil), event.LLM.ToolCalls...)
	return event
}

func ValidateCalls(ctx context.Context, before, after Event) error {
	locked, _ := ctx.Value(readOnlyCallsKey{}).(bool)
	if !locked {
		return nil
	}
	if !reflect.DeepEqual(before.LLM.ToolCalls, after.LLM.ToolCalls) || before.Tool.ID != after.Tool.ID || before.Tool.Name != after.Tool.Name || before.Tool.Arguments != after.Tool.Arguments {
		return fmt.Errorf("returned tool calls, IDs, names and arguments are read-only for this execution")
	}
	return nil
}
