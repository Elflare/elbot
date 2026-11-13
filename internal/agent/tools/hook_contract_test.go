package tools

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/llm"
)

type toolOnlyHooks struct{}

func (toolOnlyHooks) ToolCallPrepared(_ context.Context, _ agentshare.Session, call llm.ToolCallRequest) (string, error) {
	return call.Arguments, nil
}

func (toolOnlyHooks) ToolCallCompleted(_ context.Context, _ agentshare.Session, result agentshare.ToolResult) ([]llm.MessageSegment, error) {
	return result.Result.Data, nil
}

// Compile-time contracts: tool execution must not require input, model or output callbacks.
var (
	_ = ToolRunDeps{Hooks: toolOnlyHooks{}}
	_ = toolRunDeps{hooks: toolOnlyHooks{}}
)
