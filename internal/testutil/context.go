package testutil

import (
	"context"

	"elbot/internal/contextinfo"
)

// WithCLIConversation 显式构造本地 CLI 请求来源，保留调用方的取消链和其他上下文。
func WithCLIConversation(ctx context.Context) context.Context {
	return contextinfo.WithConversation(ctx, contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "cli", ChatType: contextinfo.ChatPrivate, ChatID: "local"},
		Identity: contextinfo.Identity{ID: "local"},
	})
}

// TaskContext 构造测试用后台执行事实，保留已有 Request/Run 标识。
func TaskContext(ctx context.Context, taskType string) context.Context {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.Mode, facts.TaskType = "background", taskType
	return contextinfo.WithExecution(ctx, facts)
}
func ForegroundContext(ctx context.Context) context.Context {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.Mode, facts.TaskType, facts.TaskID = "work", "", ""
	return contextinfo.WithExecution(ctx, facts)
}
func ExecutionMode(ctx context.Context) string {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	return facts.Mode
}
