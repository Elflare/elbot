package dialogue

import (
	"context"

	"elbot/internal/agent/hooks"
	"elbot/internal/events"
)

type preparationOnlyHooks struct{}

func (preparationOnlyHooks) LLMTurnPrepared(_ context.Context, _ hooks.Session, input hooks.LLMInput) (hooks.LLMInputResult, events.LogResult, error) {
	return hooks.LLMInputResult{Segments: input.Message.Segments, Tools: input.Tools}, events.LogResult{}, nil
}

type callOnlyHooks struct{}

func (callOnlyHooks) LLMRequestPrepared(_ context.Context, _ hooks.Session, input hooks.LLMInput) (hooks.LLMInputResult, events.LogResult, error) {
	return hooks.LLMInputResult{Segments: input.Message.Segments, Tools: input.Tools}, events.LogResult{}, nil
}

func (callOnlyHooks) LLMResponseReceived(_ context.Context, _ hooks.Session, response hooks.LLMResponse) (hooks.LLMResponseResult, events.LogResult, error) {
	return hooks.LLMResponseResult{Text: response.Text, SourceText: response.SourceText, Usage: response.Usage, ToolCalls: response.ToolCalls}, events.LogResult{}, nil
}

func (callOnlyHooks) ErrorOccurred(context.Context, hooks.Session, hooks.LLMResponse, error) {}

type runnerOnlyHooks struct {
	preparationOnlyHooks
	callOnlyHooks
}

// Compile-time contracts: each collaborator consumes its own subset; assembly
// needs only their union, never the input, tool or output points.
var (
	_ = Preparer{Hooks: preparationOnlyHooks{}}
	_ = CallProcessor{Hooks: callOnlyHooks{}}
	_ = RunnerDeps{Hooks: runnerOnlyHooks{}}
)
