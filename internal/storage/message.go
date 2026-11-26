package storage

import (
	"context"
	"time"

	"elbot/internal/contextinfo"
)

const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

type Message struct {
	ID                       string
	SessionID                string
	Role                     string
	Content                  string
	SourceContent            string
	ReasoningContent         string
	ParentMessageID          string
	ReplyToPlatformMessageID string
	ReplyToMessageID         string
	ToolCallID               string
	ToolCalls                []ToolCall
	ToolName                 string
	ToolBatchStatus          string
	ToolResultStatus         string
	Segments                 string
	Metadata                 string
	CreatedAt                time.Time
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ContextSummary struct {
	ID            string
	SessionID     string
	FromMessageID string
	ToMessageID   string
	Summary       string
	Provider      string
	Model         string
	// SourceTokens is supplied by the compactor; nil persists as SQL NULL (unknown).
	SourceTokens *int64
	// SummaryTokens is the reported output count; non-nil zero is an explicit zero.
	SummaryTokens *int64
	// TotalTokens is the reported total; nil is unknown and is never inferred.
	TotalTokens *int64
	// CacheHitTokens is the reported cache count; nil persists as SQL NULL.
	CacheHitTokens *int64
	TriggerReason  string
	Metadata       string
	CreatedAt      time.Time
}

type ToolCallRecord struct {
	SessionID  string
	ToolCallID string
	ToolName   string
	Actor      contextinfo.ActorIdentity
	RiskLevel  string
	Success    bool
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
	CreatedAt  time.Time
}

type ToolUsageSummary struct {
	ToolName string
	Count    int
}

type PlatformMessageMap struct {
	Platform          string
	ChatType          contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID            string
	PlatformMessageID string
	MessageID         string
	SessionID         string
	CreatedAt         time.Time
}

// MessageStats 只读取会话消息计数和最近正文，不加载完整历史。
type MessageStats struct {
	Count                int    // 会话自己的持久化消息数量。
	LastUserContent      string // 最近一条非空用户正文。
	LastAssistantContent string // 最近一条非空助手正文。
}

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
