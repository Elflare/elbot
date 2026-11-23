// reference.go 定义适配器与引用恢复组件之间的内部契约。
package share

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
)

// ReferencedMessage 保留引用内容及原始媒体顺序。
type ReferencedMessage struct {
	SenderID         string           // 引用消息发送者 ID。
	SenderName       string           // 引用消息发送者展示名。
	IsAssistant      bool             // 已知来自助手输出。
	Text             string           // 引用正文。
	Segments         []MessageSegment // 原始有序片段，用于历史媒体关联。
	CanonicalContent bool             // 已是持久化助手正文，不再抓取并重复注入。
}

// ReferenceOptions 是单次引用恢复输入；存储依赖由根部固定注入。
type ReferenceOptions struct {
	Bot      Identity                  // 当前平台的机器人身份，用于识别引用作者和补全展示名。
	Platform string                    // 平台名称。
	ChatType contextinfo.ChatType      // 会话类型，与原生 ID 共同标识会话。
	ChatID   string                    // 聊天范围。
	Owner    contextinfo.ActorIdentity // 当前行为者。

	Segments   []MessageSegment                                                   // 当前输入的有序展示片段。
	EventQuote ReferencedMessage                                                  // 入站事件已携带的引用内容，不依赖消息 ID 或二次查询。
	ReplyID    string                                                             // 被引用的消息 ID。
	Text       string                                                             // 当前输入正文。
	Fetch      func(context.Context) (ReferencedMessage, bool, error)             // 可选协议查询。
	Enrich     func(context.Context, ReferencedMessage) ([]MessageSegment, error) // 可选展示加工，不改变原始媒体序号。
}

type ReferenceResult struct {
	Text              string           // 处理后的当前正文。
	ForkFromMessageID string           // 可选会话分叉来源。
	ResumeSessionID   string           // 可选恢复会话。
	DisplaySegments   []MessageSegment // 给模型的完整有序输入。
	Reply             ReplyContext     // 原始引用事实。
}

// ApplyReference 回填引用解析的公共结果，协议特有的发送者修正由适配器处理。
func (m *MessageContext) ApplyReference(ref ReferenceResult) {
	m.ForkFromMessageID = ref.ForkFromMessageID
	m.ResumeSessionID = ref.ResumeSessionID
	m.ContextText = ref.Text
	m.ContextSegments = ref.DisplaySegments
	m.Reply = ref.Reply
}

func LogContextFailure(ctx context.Context, platform string, err error) {
	events.Log(context.WithoutCancel(ctx), events.LogRecord{
		Category: events.LogCategoryLog, Level: slog.LevelError, EventName: "platform_context_failed",
		Module: platform, Type: events.ContentText, Content: fmt.Sprintf("prepare platform context failed: %v", err),
		ContentLimit: 4096, ResultStatus: events.ResultFailed,
	})
}

// FormatReferenceSegments 只加引用标记，保留正文、媒体和展开片段的顺序。
func FormatReferenceSegments(reply ReplyContext, body []MessageSegment, expanded bool) []MessageSegment {
	header := "引用"
	if reply.HistorySeq != 0 {
		header = "引用 h" + strconv.FormatInt(reply.HistorySeq, 10)
	}
	name := strings.TrimSpace(reply.SenderName)
	id := strings.TrimSpace(reply.SenderID)
	if reply.IsAssistant {
		if name == "" {
			name = "助手"
		} else {
			name += "（助手）"
		}
	} else if id != "" {
		if name != "" {
			name += "(id:" + id + ")"
		} else {
			name = "id:" + id
		}
	}
	if name != "" {
		header += "：" + name
	} else if reply.HistorySeq == 0 {
		header += " 未知用户"
	}
	if expanded {
		return FinalMessageSegments(nil, "["+header+"]\n", body)
	}
	if len(body) > 0 {
		header += "："
		if SegmentsText(body) == "" {
			header += "[媒体消息]"
		}
	}
	return FinalMessageSegments(FinalMessageSegments(nil, "["+header, body), "]", nil)
}
