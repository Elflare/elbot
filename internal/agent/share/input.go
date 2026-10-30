package share

import (
	"context"
	"errors"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"slices"
)

// MessageContext 只保存 Agent 输入和运行选择；会话事实始终从 contextinfo 读取。
type MessageContext struct {
	RawText               string                                                                     // 平台原始正文。
	GroupRole             contextinfo.GroupRole                                                      // 输入行为者的群身份。
	BufferAssistantOutput bool                                                                       // 是否缓冲完整助手输出。
	ForkFromMessageID     string                                                                     // 分叉来源消息。
	ResumeSessionID       string                                                                     // 恢复的会话。
	Segments              []MessageSegment                                                           // 当前输入片段。
	ContextText           string                                                                     // 引用上下文正文。
	ContextSegments       []MessageSegment                                                           // 引用上下文片段。
	Reply                 ReplyContext                                                               // 原始引用事实。
	Bot                   Identity                                                                   // 平台机器人身份。
	Mentions              []Mention                                                                  // 提及事实。
	TriggerKeywords       []string                                                                   // 平台配置的唤醒词。
	ResolveMedia          func(context.Context, media.PlatformMedia, int64) (delivery.Source, error) // 延迟媒体解析。
	RestoreMessageContext func(context.Context, context.Context) context.Context                     // 根部注入的平台上下文恢复，内部不解释平台值。
}

type MessageSegment struct {
	Type   string              // text、image、file 或 at。
	Text   string              // 正文或附件说明。
	UserID string              // 提及用户标识。
	Media  media.PlatformMedia // 媒体来源和物化结果。
}

type ReplyContext struct {
	MessageID  string           // 被引用的平台消息标识。
	SenderID   string           // 引用发送者标识。
	SenderName string           // 引用发送者展示名。
	Text       string           // 引用正文。
	Segments   []MessageSegment // 引用原始片段。
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

type messageContextKey struct{}

func cloneMessageContext(msg MessageContext) MessageContext {
	msg.Segments = slices.Clone(msg.Segments)
	msg.ContextSegments = slices.Clone(msg.ContextSegments)
	msg.Reply.Segments = slices.Clone(msg.Reply.Segments)
	msg.Mentions = slices.Clone(msg.Mentions)
	msg.TriggerKeywords = slices.Clone(msg.TriggerKeywords)
	return msg
}

func WithMessageContext(ctx context.Context, msg MessageContext) context.Context {
	return context.WithValue(ctx, messageContextKey{}, cloneMessageContext(msg))
}

func MessageContextFrom(ctx context.Context) (MessageContext, bool) {
	msg, ok := ctx.Value(messageContextKey{}).(MessageContext)
	return cloneMessageContext(msg), ok
}

func WithoutMessageContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, messageContextKey{}, struct{}{})
}

func RestoreMessageContext(ctx, source context.Context) context.Context {
	current, _ := MessageContextFrom(ctx)
	if current.RestoreMessageContext != nil {
		ctx = current.RestoreMessageContext(ctx, source)
	}
	if msg, ok := MessageContextFrom(source); ok {
		return WithMessageContext(ctx, msg)
	}
	return WithoutMessageContext(ctx)
}

// StripTriggerKeyword 只移除命中的前缀，唤醒规则归 Agent。
func StripTriggerKeyword(text string, keywords []string) (string, bool) {
	text = strings.TrimSpace(text)
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword != "" && strings.HasPrefix(text, keyword) {
			return strings.TrimSpace(strings.TrimPrefix(text, keyword)), true
		}
	}
	return text, false
}

// InputDisposition directs prepared input to execution or risk confirmation.
// It does not transfer ownership of the active Turn or its confirmation state.
type InputDisposition uint8

const (
	InputHandled InputDisposition = iota
	InputRiskConfirmation
)

var ErrInputCompacting = errors.New("compacting · 等待或 /stop")

var ErrInputArchived = errors.New("会话已归档 · /unarchive 后继续")

func StripWakeupPrefix(ctx context.Context, text string) string {
	if msg, ok := MessageContextFrom(ctx); ok {
		return stripWakeupPrefixFromText(text, msg)
	}
	return text
}

func stripWakeupPrefixFromText(text string, msg MessageContext) string {
	if stripped, ok := StripTriggerKeyword(text, msg.TriggerKeywords); ok {
		return stripped
	}
	if before, after, ok := strings.Cut(text, "\n\n"); ok {
		stripped := stripWakeupPrefixFromText(after, msg)
		if stripped != after {
			return before + "\n\n" + stripped
		}
	}
	return stripBotMention(text, msg)
}

func stripBotMention(text string, msg MessageContext) string {
	botUsername := strings.TrimPrefix(strings.TrimSpace(msg.Bot.Username), "@")
	if botUsername == "" || text == "" {
		return text
	}
	fields := strings.Fields(text)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "/") {
		if name, mention, ok := strings.Cut(fields[0], "@"); ok && strings.EqualFold(mention, botUsername) {
			fields[0] = name
			return strings.TrimSpace(strings.Join(fields, " "))
		}
	}
	kept := fields[:0]
	for _, field := range fields {
		if strings.EqualFold(field, "@"+botUsername) {
			continue
		}
		kept = append(kept, field)
	}
	return strings.TrimSpace(strings.Join(kept, " "))
}
