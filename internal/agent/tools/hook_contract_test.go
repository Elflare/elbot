package tools

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/llm"
)

type toolOnlyHooks struct{}

func (toolOnlyHooks) ToolCallPrepared(_ context.Context, _ agentshare.Session, call llm.ToolCallRequest) (string, events.LogResult, error) {
	return call.Arguments, events.LogResult{}, nil
}

func (toolOnlyHooks) ToolCallCompleted(_ context.Context, _ agentshare.Session, result agentshare.ToolResult) ([]llm.MessageSegment, events.LogResult, error) {
	return result.Segments, events.LogResult{}, nil
}

// Compile-time contracts: tool execution must not require input, model or output callbacks.
var (
	_ = ToolRunDeps{Hooks: toolOnlyHooks{}}
	_ = toolRunDeps{hooks: toolOnlyHooks{}}
)
