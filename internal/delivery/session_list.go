package delivery

import (
	"fmt"
	"strings"
	"time"

	"elbot/internal/llm"
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
	InputIDs                 []string             `json:"input_ids,omitempty"`
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

func SessionListText(d ListSessionsResult) string {
	var out strings.Builder
	for i, item := range d.Items {
		if i > 0 {
			out.WriteByte('\n')
		}
		title := item.Title
		if title == "" {
			title = "(untitled)"
		}
		markers := []string{}
		if item.ID == d.CurrentSessionID {
			markers = append(markers, "current")
		}
		if item.PinnedAt != nil {
			markers = append(markers, "pinned")
		}
		if item.ArchivedAt != nil {
			markers = append(markers, "archived")
		}
		mark := ""
		if len(markers) > 0 {
			mark = " [" + strings.Join(markers, ", ") + "]"
		}
		fmt.Fprintf(&out, "[%s] %s%s\n%s/%s · %s · msgs=%d\n", item.ID, title, mark, item.Platform, item.ChatID, item.UpdatedAt.Format("2006-01-02 15:04:05"), item.MessageCount)
		var previews []string
		if len(item.Messages) > 4 {
			previews = append(previews, "...")
		}
		for _, message := range item.Messages[max(0, len(item.Messages)-4):] {
			content := MessagePreview(message, 24)
			if content == "" {
				continue
			}
			role := "user"
			if string(message.Role) == "assistant" {
				role = "bot"
			}
			previews = append(previews, role+": "+content)
		}
		if len(previews) > 0 {
			out.WriteString(strings.Join(previews, " / ") + "\n")
		}
	}
	if out.Len() > 0 {
		out.WriteString("\n")
	}
	fmt.Fprintf(&out, "page=%d/%d", d.Page.Page, d.TotalPages)
	return out.String()
}

func MessagePreview(message MessageData, n int) string {
	parts := []string{}
	if text := Preview(message.Content, n); text != "" {
		parts = append(parts, text)
	}
	for _, attachment := range message.Segments {
		if attachment.Type != llm.SegmentFile && attachment.Type != llm.SegmentImage {
			continue
		}
		if attachment.Name == "" {
			parts = append(parts, "[附件]")
		} else {
			parts = append(parts, "[附件: "+attachment.Name+"]")
		}
	}
	return strings.Join(parts, " ")
}

func Preview(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if n > 0 && len(runes) > n {
		return string(runes[:n]) + "..."
	}
	return text
}
