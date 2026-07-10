// Package hook defines Hook events, matching, registration, and execution.
package hook

import (
	"context"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/llm"
)

// Point names a stable boundary where hooks may inspect or update an event.
type Point string

const (
	PointPlatformConnected       Point = "platform.connected"
	PointPlatformMessageReceived Point = "platform.message.received"
	PointAgentInputPrepared      Point = "agent.input.prepared"
	PointLLMTurnPrepared         Point = "llm.turn.prepared"
	PointLLMRequestPrepared      Point = "llm.request.prepared"
	PointLLMResponseReceived     Point = "llm.response.received"
	PointToolCallPrepared        Point = "tool.call.prepared"
	PointToolCallCompleted       Point = "tool.call.completed"
	PointAgentOutputPrepared     Point = "agent.output.prepared"
	PointAgentTurnOutputPrepared Point = "agent.turn.output.prepared"
	PointPlatformMessageSent     Point = "platform.message.sent"
	PointErrorOccurred           Point = "error.occurred"
)

func KnownPoint(point Point) bool {
	switch point {
	case PointPlatformConnected,
		PointPlatformMessageReceived,
		PointAgentInputPrepared,
		PointLLMTurnPrepared,
		PointLLMRequestPrepared,
		PointLLMResponseReceived,
		PointToolCallPrepared,
		PointToolCallCompleted,
		PointAgentOutputPrepared,
		PointAgentTurnOutputPrepared,
		PointPlatformMessageSent,
		PointErrorOccurred:
		return true
	default:
		return false
	}
}

// Event carries the context available at a hook point. Fields are populated
// according to Point; hook handlers should only rely on fields relevant there.
type Event struct {
	ID       string         `json:"id"`
	Point    Point          `json:"point"`
	Time     time.Time      `json:"time"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Control  Control        `json:"control"`

	Platform  PlatformContext   `json:"platform"`
	Actor     ActorContext      `json:"actor"`
	Session   SessionContext    `json:"session"`
	Request   RequestContext    `json:"request"`
	Message   MessagePayload    `json:"message"`
	LLM       LLMPayload        `json:"llm"`
	Tool      ToolPayload       `json:"tool"`
	Outputs   []delivery.Output `json:"outputs,omitempty"`
	Error     error             `json:"-"`
	ErrorInfo *ErrorPayload     `json:"error,omitempty"`
}

type Control struct {
	Consume         bool `json:"consume"`
	StopPropagation bool `json:"stop_propagation"`
}

type ErrorPayload struct {
	Message string `json:"message"`
}

type PlatformContext struct {
	Name              string `json:"name"`
	ScopeID           string `json:"scope_id"`
	UserID            string `json:"user_id"`
	ConversationID    string `json:"conversation_id"`
	PlatformMessageID string `json:"message_id"`
	ReplyToMessageID  string `json:"reply_to_message_id"`
}

type ActorContext struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	GroupRole   string `json:"group_role"`
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

type SessionContext struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type RequestContext struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	Phase     string `json:"phase"`
}

type MessagePayload struct {
	ID           string               `json:"id"`
	Role         string               `json:"role"`
	PlatformText string               `json:"platform_text,omitempty"`
	IntentText   string               `json:"intent_text,omitempty"`
	Reply        *MessageReplyPayload `json:"reply,omitempty"`
	Segments     []llm.MessageSegment `json:"segments,omitempty"`
	Messages     []llm.LLMMessage     `json:"messages,omitempty"`
}

type MessageReplyPayload struct {
	MessageID   string               `json:"message_id"`
	SenderID    string               `json:"sender_id,omitempty"`
	Text        string               `json:"text,omitempty"`
	DisplayText string               `json:"display_text,omitempty"`
	Segments    []llm.MessageSegment `json:"segments,omitempty"`
}

type LLMPayload struct {
	Provider   string                `json:"provider"`
	Model      string                `json:"model"`
	Messages   []llm.LLMMessage      `json:"messages,omitempty"`
	Tools      []llm.ToolSchema      `json:"tools,omitempty"`
	Usage      *llm.Usage            `json:"usage,omitempty"`
	SourceText string                `json:"source_text,omitempty"`
	Text       string                `json:"text,omitempty"`
	ToolCalls  []llm.ToolCallRequest `json:"tool_calls,omitempty"`
	ElapsedMS  int64                 `json:"elapsed_ms,omitempty"`
}

type ToolPayload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	Risk      string `json:"risk,omitempty"`
	Result    string `json:"result,omitempty"`
	Error     error  `json:"error,omitempty"`
}

// Handler processes one hook event and may return an updated event.
type Handler interface {
	HandleHook(ctx context.Context, event Event) (Event, error)
}

type HandlerFunc func(ctx context.Context, event Event) (Event, error)

func (fn HandlerFunc) HandleHook(ctx context.Context, event Event) (Event, error) {
	return fn(ctx, event)
}

func prepareEvent(event Event) Event {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	if event.Metadata == nil {
		event.Metadata = map[string]any{}
	}
	if event.ErrorInfo == nil && event.Error != nil {
		event.ErrorInfo = &ErrorPayload{Message: event.Error.Error()}
	}
	return event
}

func EventErrorMessage(event Event) string {
	if event.ErrorInfo != nil {
		return event.ErrorInfo.Message
	}
	if event.Error != nil {
		return event.Error.Error()
	}
	return ""
}

func MessageIntentText(event Event) string {
	if strings.TrimSpace(event.Message.IntentText) != "" {
		return strings.TrimSpace(event.Message.IntentText)
	}
	return llm.SegmentsTextOnly(event.Message.Segments)
}
