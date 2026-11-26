package storage

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/storage/maindb"
)

type (
	Message            = maindb.Message
	ContextSummary     = maindb.ContextSummary
	ToolCallRecord     = maindb.ToolCallRecord
	ToolUsageSummary   = maindb.ToolUsageSummary
	PlatformMessageMap = maindb.PlatformMessageMap
	MessageStats       = maindb.MessageStats
)

type MessageRepository interface {
	MessageStats(context.Context, string) (MessageStats, error)
	CountConversationMessages(ctx context.Context, sessionID, toMessageID string) (int, error)                         // 对指定分支范围的对话及工具记录计数。
	ListInputHistory(ctx context.Context, account contextinfo.ActorIdentity, limit int) ([]string, error)              // 最近的非空用户正文，先限量再按原文去重。
	ListConversationMessages(ctx context.Context, sessionID, toMessageID string, limit, offset int) ([]Message, error) // 限量倒序读取原始对话及工具记录，不应用压缩摘要。
	AppendMessage(ctx context.Context, message *Message) error
	GetMessage(ctx context.Context, id string) (*Message, error)
	ListSessionMessages(ctx context.Context, sessionID string) ([]Message, error)
	ListBySessionUpTo(ctx context.Context, sessionID, toMessageID string) ([]Message, error)
	ListBySessionAfter(ctx context.Context, sessionID, afterMessageID string) ([]Message, error)
	ListBySessionAfterUpTo(ctx context.Context, sessionID, afterMessageID, toMessageID string) ([]Message, error)
	FindByPlatformMessage(ctx context.Context, platform string, conversationKind contextinfo.ChatType, scopeID string, platformMessageID string) (*Message, error)
	PlatformMessageIDs(context.Context, string, contextinfo.ChatType, string, string) ([]string, error)
}
