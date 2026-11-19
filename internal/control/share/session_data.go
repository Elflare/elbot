package share

import (
	"elbot/internal/llm"
	"time"
)

type Page struct {
	Page       int  `json:"page"`        // 从 1 开始。
	PageSize   int  `json:"page_size"`   // 每页容量。
	HasNext    bool `json:"has_next"`    // 是否还有更早/下一页记录。
	TotalPages int  `json:"total_pages"` // 空列表为 0。
}
type SessionData struct {
	ID                string     `json:"id"`
	Title             string     `json:"title"`
	Mode              string     `json:"mode"`
	Status            string     `json:"status"`
	Platform          string     `json:"platform"`
	ChatID            string     `json:"chat_id"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	ArchivedAt        *time.Time `json:"archived_at"`
	PinnedAt          *time.Time `json:"pinned_at"`
	ParentSessionID   string     `json:"parent_session_id"`
	ForkFromMessageID string     `json:"fork_from_message_id"`
}
type MessageData struct {
	ID                       string               `json:"id"`
	SessionID                string               `json:"session_id"`
	Role                     string               `json:"role"`
	Content                  string               `json:"content"`
	SourceContent            string               `json:"source_content,omitempty"`
	ReasoningContent         string               `json:"reasoning_content"`
	ParentMessageID          string               `json:"parent_message_id"`
	ReplyToPlatformMessageID string               `json:"reply_to_platform_message_id"`
	ReplyToMessageID         string               `json:"reply_to_message_id"`
	ToolCallID               string               `json:"tool_call_id"`
	Segments                 []llm.MessageSegment `json:"segments"` // 保留原有顺序与媒体引用；出口自行处理展示与授权。
	ToolCalls                []ToolCallData       `json:"tool_calls"`
	ToolName                 string               `json:"tool_name,omitempty"`
	ToolBatchStatus          string               `json:"tool_batch_status,omitempty"`
	ToolResultStatus         string               `json:"tool_result_status,omitempty"`
	CreatedAt                time.Time            `json:"created_at"`
}
type ToolCallData struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type SessionSummaryData struct {
	SessionData
	MessageCount         int           `json:"message_count"`
	Messages             []MessageData `json:"messages"`
	LastUserContent      string        `json:"last_user_content"`
	LastAssistantContent string        `json:"last_assistant_content"`
}
type ListSessionsResult struct {
	Page
	Items            []SessionSummaryData `json:"items"`
	CurrentSessionID string               `json:"current_session_id"`
	Query            string               `json:"query"`
}
