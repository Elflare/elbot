package testutil

import (
	"context"

	"elbot/internal/contextinfo"
)

// WithCLIConversation 显式构造本地 CLI 请求来源，保留调用方的取消链和其他上下文。
func WithCLIConversation(ctx context.Context) context.Context {
	return contextinfo.WithConversation(ctx, contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "cli", ScopeID: "local", ConversationKind: contextinfo.ConversationCLI, ConversationID: "local"},
		Identity: contextinfo.Identity{PlatformUserID: "local"},
	})
}
