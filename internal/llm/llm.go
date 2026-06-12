package llm

import "context"

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

// ToolCallRequest represents a tool call the model wants to make.
type ToolCallRequest struct {
	ID        string
	Name      string
	Arguments string
}

type ToolSchema struct {
	Type     string             `json:"type"`
	Function ToolFunctionSchema `json:"function"`
}

type ToolFunctionSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ChatRequest is the input for an LLM chat call.
type ChatRequest struct {
	Model       string
	SessionID   string
	Messages    []LLMMessage
	Tools       []ToolSchema
	Temperature float64
	MaxTokens   int
	// ExtraBody contains additional fields merged into the request JSON.
	// It has the highest priority and can override any other field.
	ExtraBody map[string]any
}

// StreamChunk is a single chunk of a streaming response.
type StreamChunk struct {
	DeltaContent          string
	DeltaReasoningContent string
	ToolCallDeltas        []ToolCallDelta
	FinishReason          string
	Usage                 *Usage
	Error                 error
}

// ToolCallDelta is an incremental tool call fragment from a stream.
type ToolCallDelta struct {
	Index int
	ID    string
	Name  string
	Args  string // incremental JSON fragment
}

// Usage contains token usage statistics.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CacheHitTokens   int
}

type ModelMetadata struct {
	ID            string
	ContextWindow int
}

type ModelMetadataProvider interface {
	ListModelMetadata(ctx context.Context) ([]ModelMetadata, error)
}

// ChatResponse is the complete response from a non-streaming chat call.
type ChatResponse struct {
	Message   LLMMessage
	ToolCalls []ToolCallRequest
	Usage     *Usage
}

// LLM is the interface for a language model adapter.
type LLM interface {
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)
	ListModels(ctx context.Context) ([]string, error)
}
