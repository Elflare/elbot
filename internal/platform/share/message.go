// message.go 定义内部消息、片段、引用与消息上下文。

package share

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

type MessageSegmentType string

const (
	SegmentText  MessageSegmentType = "text"
	SegmentImage MessageSegmentType = "image"
	SegmentFile  MessageSegmentType = "file"
	SegmentAt    MessageSegmentType = "at"
)

// MessageSegment 是平台处理、存储和公开引用事实共用的有序片段，不携带运行能力。
type MessageSegment struct {
	Type           MessageSegmentType `json:"type"`                       // 片段种类。
	Text           string             `json:"text,omitempty"`             // 文本或附件说明。
	UserID         string             `json:"user_id,omitempty"`          // 提及用户标识。
	URL            string             `json:"url,omitempty"`              // 媒体来源地址。
	MediaID        string             `json:"media,omitempty"`            // 已导入的媒体 ID。
	PlatformFileID string             `json:"platform_file_id,omitempty"` // 平台文件标识。
	MIMEType       string             `json:"mime_type,omitempty"`        // 内容类型。
	Name           string             `json:"name,omitempty"`             // 文件名。
	Size           int64              `json:"size,omitempty"`             // 已知字节数。
}

type ReplyContext struct {
	Display     string           // 入站引用展示快照，不要求存在本地历史短号。
	MessageID   string           // 被引用的平台消息 ID。
	HistorySeq  int64            // 当前聊天中被引用记录的短号；未入库时为零。
	IsAssistant bool             // 被引用消息由当前机器人发出。
	SenderID    string           // 被引用消息的发送者 ID。
	SenderName  string           // 发送者展示名。
	Text        string           // 引用正文。
	Segments    []MessageSegment // 引用消息片段。
}

// SegmentsText 只投影正文和提及，不把媒体描述当成用户文字。
func SegmentsText(segments []MessageSegment) string {
	var text strings.Builder
	for _, s := range segments {
		if s.Type == SegmentText || s.Type == SegmentAt {
			text.WriteString(s.Text)
		}
	}
	return text.String()
}

// FinalMessageSegments 在原有片段之间插入展示文字；不移动媒体，也不修改输入。
func FinalMessageSegments(before []MessageSegment, text string, after []MessageSegment) []MessageSegment {
	out := make([]MessageSegment, 0, len(before)+len(after)+1)
	var words strings.Builder
	flush := func() {
		if words.Len() > 0 {
			out = append(out, MessageSegment{Type: SegmentText, Text: words.String()})
			words.Reset()
		}
	}
	add := func(parts []MessageSegment) {
		for _, s := range parts {
			if s.Type == SegmentText {
				words.WriteString(s.Text)
			} else {
				flush()
				out = append(out, s)
			}
		}
	}
	add(before)
	words.WriteString(text)
	add(after)
	flush()
	return out
}

// MediaResolver 仅表达协议侧媒体来源解析，不负责媒体缓存。
type MediaResolver interface {
	ResolveMedia(context.Context, MessageSegment, int64) (operationresult.Result[delivery.Source], error)
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
	CommandReply interface {
		SendCommandResult(context.Context, command.Result, error) (delivery.Receipt, error)
	} // 可选的结构化命令回复能力；未提供时由根部发送文字。
	contextinfo.Conversation                        // 读写边界投影的公共会话事实。
	GroupRole                contextinfo.GroupRole  // 入站行为者的群身份。
	Sender                   delivery.MessageSender // 当前协议消息的回复通道。
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
