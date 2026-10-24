package input

import (
	"context"

	"elbot/internal/agent/hooks"
	"elbot/internal/events"
	"elbot/internal/llm"
)

type inputOnlyHooks struct{}

func (inputOnlyHooks) AgentInputPrepared(_ context.Context, _ hooks.Session, message hooks.Message) ([]llm.MessageSegment, events.LogResult, error) {
	return message.Segments, events.LogResult{}, nil
}

// Compile-time contracts: input preparation must not require other hook points.
var (
	_ = InputCoordinatorDeps{Hooks: inputOnlyHooks{}}
	_ = InputCoordinator{hooks: inputOnlyHooks{}}
)
