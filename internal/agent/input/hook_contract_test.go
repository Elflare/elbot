package input

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/llm"
)

type inputOnlyHooks struct{}

func (inputOnlyHooks) AgentInputPrepared(_ context.Context, _ agentshare.Session, message agentshare.Message) ([]llm.MessageSegment, events.LogResult, error) {
	return message.Segments, events.LogResult{}, nil
}

// Compile-time contracts: input preparation must not require other hook points.
var (
	_ = InputCoordinatorDeps{Hooks: inputOnlyHooks{}}
	_ = InputCoordinator{hooks: inputOnlyHooks{}}
)
