// reference.go 定义适配器与引用恢复组件之间的内部契约。
package share

import (
	"context"
	"strings"
)

// ReferencedMessage 保留引用内容及原始媒体顺序。
type ReferencedMessage struct {
	SenderID         string           // 引用消息发送者 ID。
	SenderName       string           // 引用消息发送者展示名。
	Label            string           // 引用展示标签。
	Text             string           // 引用正文。
	Segments         []MessageSegment // 原始有序片段，用于历史媒体关联。
	CanonicalContent bool             // 已是持久化助手正文，不再抓取并重复注入。
}

// ReferenceOptions 是单次引用恢复输入；存储依赖由根部固定注入。
type ReferenceOptions struct {
	Platform     string                                                                     // 平台名称。
	ScopeID      string                                                                     // 聊天范围。
	ActorID      string                                                                     // 当前行为者。
	IsSuperadmin bool                                                                       // 是否允许接管后台会话。
	ReplyID      string                                                                     // 被引用的消息 ID。
	Text         string                                                                     // 当前输入正文。
	Fetch        func(context.Context, string) (ReferencedMessage, bool, error)             // 可选协议查询。
	Enrich       func(context.Context, string, ReferencedMessage) ([]MessageSegment, error) // 可选展示加工，不改变原始媒体序号。
}

type ReferenceResult struct {
	Text              string           // 处理后的当前正文。
	ForkFromMessageID string           // 可选会话分叉来源。
	ResumeSessionID   string           // 可选恢复会话。
	ReferenceSegments []MessageSegment // 引用上下文片段。
	DisplaySegments   []MessageSegment // 可选引用展示片段。
	Reply             ReplyContext     // 原始引用事实。
}

func FormatReferenceText(platformName string, reply ReplyContext, currentText string) string {
	header := "引用"
	if id := strings.TrimSpace(reply.MessageID); id != "" {
		header += "#" + id
	}
	name := strings.TrimSpace(reply.SenderName)
	id := strings.TrimSpace(reply.SenderID)
	if id != "" {
		kind := referenceIDKind(platformName)
		if name != "" {
			name += "(" + kind + ":" + id + ")"
		} else {
			name = kind + ":" + id
		}
	}
	if name != "" {
		header += "：" + name
	}
	if content := strings.TrimSpace(reply.Text); content != "" {
		header += ":" + content
	}
	formatted := "[" + header + "]"
	if currentText = strings.TrimSpace(currentText); currentText != "" {
		formatted += "\n\n" + currentText
	}
	return formatted
}

func referenceIDKind(platformName string) string {
	switch strings.TrimSpace(platformName) {
	case "qqonebot":
		return "qq"
	case "telegram":
		return "tg"
	case "qqofficial":
		return "openid"
	default:
		if platformName = strings.TrimSpace(platformName); platformName != "" {
			return platformName
		}
		return "id"
	}
}
