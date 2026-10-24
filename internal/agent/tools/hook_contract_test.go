package tools

import (
	"context"

	"elbot/internal/agent/hooks"
	"elbot/internal/events"
	"elbot/internal/llm"
)

type toolOnlyHooks struct{}

func (toolOnlyHooks) ToolCallPrepared(_ context.Context, _ hooks.Session, call llm.ToolCallRequest) (string, events.LogResult, error) {
	return call.Arguments, events.LogResult{}, nil
}

func (toolOnlyHooks) ToolCallCompleted(_ context.Context, _ hooks.Session, result hooks.ToolResult) ([]llm.MessageSegment, events.LogResult, error) {
	return result.Segments, events.LogResult{}, nil
}

// Compile-time contracts: tool execution must not require input, model or output callbacks.
var (
	_ = ToolRunDeps{Hooks: toolOnlyHooks{}}
	_ = toolRunDeps{hooks: toolOnlyHooks{}}
)
