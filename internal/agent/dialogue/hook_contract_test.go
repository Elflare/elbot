package dialogue

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
)

type preparationOnlyHooks struct{}

func (preparationOnlyHooks) LLMTurnPrepared(_ context.Context, _ agentshare.Session, input agentshare.LLMInput) (agentshare.LLMInputResult, events.LogResult, error) {
	return agentshare.LLMInputResult{Segments: input.Message.Segments, Tools: input.Tools}, events.LogResult{}, nil
}

type callOnlyHooks struct{}

func (callOnlyHooks) LLMRequestPrepared(_ context.Context, _ agentshare.Session, input agentshare.LLMInput) (agentshare.LLMInputResult, events.LogResult, error) {
	return agentshare.LLMInputResult{Segments: input.Message.Segments, Tools: input.Tools}, events.LogResult{}, nil
}

func (callOnlyHooks) LLMResponseReceived(_ context.Context, _ agentshare.Session, response agentshare.LLMResponse) (agentshare.LLMResponseResult, events.LogResult, error) {
	return agentshare.LLMResponseResult{Text: response.Text, SourceText: response.SourceText, Usage: response.Usage, ToolCalls: response.ToolCalls}, events.LogResult{}, nil
}

func (callOnlyHooks) ErrorOccurred(context.Context, agentshare.Session, agentshare.LLMResponse, error) {
}

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
