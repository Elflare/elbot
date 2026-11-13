// 定义 Hook 事件点、事件数据、控制结果和 handler 契约。

// Package share defines contracts and helpers shared by Hook components.
package share

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"encoding/json"
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

// MaxProtocolFrameBytes is the maximum size of one hook.v2 JSON Lines frame.
const MaxProtocolFrameBytes = 16 * 1024 * 1024

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

	Chat      ChatContext       `json:"chat"`
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

type ChatContext struct {
	Platform          string               `json:"platform"`  // 聊天所属平台。
	ChatType          contextinfo.ChatType `json:"chat_type"` // 私聊、群聊或频道。
	ID                string               `json:"id"`        // 平台内原生聊天 ID。
	Name              string               `json:"name"`      // 展示名，不参与路由与权限判断。
	PlatformMessageID string               `json:"message_id"`
	ReplyToMessageID  string               `json:"reply_to_message_id"`
}

type ActorContext struct {
	Platform  string `json:"platform,omitempty"` // 行为者所属平台。
	Role      string `json:"role"`
	GroupRole string `json:"group_role"`
	ID        string `json:"id"`
	Nickname  string `json:"nickname"`
	GroupCard string `json:"group_card"`
	Name      string `json:"name"`
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
	ID              string               `json:"id"`
	Role            string               `json:"role"`
	PlatformText    string               `json:"platform_text,omitempty"`
	PlatformMessage json.RawMessage      `json:"platform_message,omitempty"`
	IntentText      string               `json:"intent_text,omitempty"`
	Reply           *MessageReplyPayload `json:"reply,omitempty"`
	Segments        []llm.MessageSegment `json:"segments,omitempty"`
	Messages        []llm.LLMMessage     `json:"messages,omitempty"`
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
	ID        string                         `json:"id"`
	Name      string                         `json:"name"`
	Arguments string                         `json:"arguments,omitempty"`
	Risk      string                         `json:"risk,omitempty"`
	Result    operationresult.Result[string] `json:"-"` // 工具业务结果；失败展示 Message，成功展示 Data。
	Error     string                         `json:"-"` // Go 故障的 Error() 文本，独立于业务结果。
}

func (p ToolPayload) MarshalJSON() ([]byte, error) {
	result := p.Result.Message
	if p.Result.Status == operationresult.Success {
		result = p.Result.Data
	}
	if result == "" {
		result = p.Error
	}
	return json.Marshal(struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments,omitempty"`
		Risk      string `json:"risk,omitempty"`
		Result    string `json:"result,omitempty"`
		Error     string `json:"error,omitempty"`
	}{p.ID, p.Name, p.Arguments, p.Risk, result, p.Error})
}

// Handler returns business changes and the original operation error.
// readOnlyCalls is enforced by rules after each action that may change call data.
type Handler interface {
	HandleHook(ctx context.Context, event Event, readOnlyCalls bool) (operationresult.Result[Event], error)
}

type HandlerFunc func(ctx context.Context, event Event, readOnlyCalls bool) (operationresult.Result[Event], error)

func (fn HandlerFunc) HandleHook(ctx context.Context, event Event, readOnlyCalls bool) (operationresult.Result[Event], error) {
	return fn(ctx, event, readOnlyCalls)
}

// PrepareEvent fills execution metadata and the serializable error description.
func PrepareEvent(event Event) Event {
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

// Identity 返回事件携带的身份事实，不从会话或字符串恢复身份。
func (a ActorContext) Identity() contextinfo.ActorIdentity {
	return contextinfo.ActorIdentity{Platform: a.Platform, ID: a.ID}
}
