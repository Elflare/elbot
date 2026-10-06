package llm

// MessageRole represents the role of a message in a conversation.
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

type MessageSegmentType string

const (
	SegmentText  MessageSegmentType = "text"
	SegmentImage MessageSegmentType = "image"
	SegmentFile  MessageSegmentType = "file"
)

// MessageSegment is one typed part of a chat message.
type MessageSegment struct {
	Type     MessageSegmentType `json:"type"`
	Text     string             `json:"text,omitempty"`
	URL      string             `json:"url,omitempty"`
	MediaID  string             `json:"media,omitempty"`
	MIMEType string             `json:"mime_type,omitempty"`
	Name     string             `json:"name,omitempty"`
}

// LLMMessage represents a single message in a chat conversation.
type LLMMessage struct {
	Role       MessageRole
	Segments   []MessageSegment
	Name       string
	ToolCallID string
	ToolCalls  []ToolCallRequest
}
