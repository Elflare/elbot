// message.go 定义内部消息、片段、引用与消息上下文。

package share

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"encoding/json"
	"maps"
	"slices"
)

type MessageSegmentType string

const (
	SegmentText  MessageSegmentType = "text"
	SegmentImage MessageSegmentType = "image"
	SegmentFile  MessageSegmentType = "file"
	SegmentAt    MessageSegmentType = "at"
)

// MessageSegment 是平台内部处理和存储的有序消息片段。
type MessageSegment struct {
	Type           MessageSegmentType `json:"type"`                       // 片段种类。
	Text           string             `json:"text,omitempty"`             // 文本内容。
	UserID         string             `json:"user_id,omitempty"`          // 提及用户标识。
	URL            string             `json:"url,omitempty"`              // 媒体来源地址。
	MediaID        string             `json:"media,omitempty"`            // 已导入的媒体 ID。
	PlatformFileID string             `json:"platform_file_id,omitempty"` // 平台文件标识。
	MIMEType       string             `json:"mime_type,omitempty"`        // 内容类型。
	Name           string             `json:"name,omitempty"`             // 文件名。
	Size           int64              `json:"size,omitempty"`             // 已知字节数。
}

type ReplyContext struct {
	MessageID  string           // 被引用的平台消息 ID。
	SenderID   string           // 被引用消息的发送者 ID。
	SenderName string           // 发送者展示名。
	Text       string           // 引用正文。
	Segments   []MessageSegment // 引用消息片段。
}

// MediaResolver 仅表达协议侧媒体来源解析，不负责媒体缓存。
type MediaResolver interface {
	ResolveMedia(context.Context, MessageSegment, int64) (delivery.Source, error)
}

type Identity struct {
	UserID   string // 平台用户标识。
	Username string // 平台用户名。
}

type Mention struct {
	UserID   string // 被提及的用户标识。
	Username string // 被提及的用户名。
	Text     string // 原始提及文本。
}

// MessageContext 保存平台内部路由与入站状态，公共会话事实由 contextinfo 持有。
type MessageContext struct {
	contextinfo.Conversation                        // 读写边界投影的公共会话事实。
	GroupRole                contextinfo.GroupRole  // 入站行为者的群身份。
	Sender                   delivery.ContextSender // 当前协议消息的回复通道。
	BufferAssistantOutput    bool                   // 是否缓冲完整助手输出。
	ForkFromMessageID        string                 // 分叉来源消息。
	ResumeSessionID          string                 // 恢复的会话。
	Segments                 []MessageSegment       // 当前输入片段。
	ContextText              string                 // 引用上下文正文。
	ContextSegments          []MessageSegment       // 引用上下文片段。
	Reply                    ReplyContext           // 原始引用事实。
	Meta                     map[string]any         // 协议侧附加数据。
	RawText                  string                 // 平台原始正文。
	PlatformMessage          json.RawMessage        // 平台原始消息 JSON。
	Bot                      Identity               // 平台机器人身份。
	Mentions                 []Mention              // 提及事实。
	TriggerKeywords          []string               // 平台配置的唤醒词。
	MediaResolver            MediaResolver          // 协议侧媒体解析能力。
}

type messageContextKey struct{}

func cloneMessageContext(msg MessageContext) MessageContext {
	msg.Segments = slices.Clone(msg.Segments)
	msg.ContextSegments = slices.Clone(msg.ContextSegments)
	msg.Reply.Segments = slices.Clone(msg.Reply.Segments)
	msg.Mentions = slices.Clone(msg.Mentions)
	msg.TriggerKeywords = slices.Clone(msg.TriggerKeywords)
	msg.PlatformMessage = slices.Clone(msg.PlatformMessage)
	msg.Meta = maps.Clone(msg.Meta)
	return msg
}

func WithMessageContext(ctx context.Context, msg MessageContext) context.Context {
	ctx = contextinfo.WithConversation(ctx, msg.Conversation)
	msg.Conversation = contextinfo.Conversation{}
	return context.WithValue(ctx, messageContextKey{}, cloneMessageContext(msg))
}

func MessageContextFrom(ctx context.Context) (MessageContext, bool) {
	msg, ok := ctx.Value(messageContextKey{}).(MessageContext)
	if ok {
		msg.Conversation, _ = contextinfo.ConversationFromContext(ctx)
	}
	return cloneMessageContext(msg), ok
}

func WithoutMessageContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, messageContextKey{}, struct{}{})
}

// RestoreMessageContext 只恢复平台私有状态，不修改目标的公共会话事实或取消信号。
func RestoreMessageContext(ctx, source context.Context) context.Context {
	msg, ok := source.Value(messageContextKey{}).(MessageContext)
	if !ok {
		return WithoutMessageContext(ctx)
	}
	return context.WithValue(ctx, messageContextKey{}, cloneMessageContext(msg))
}
