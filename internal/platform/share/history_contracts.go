// history_contracts.go 定义平台内部共用的最小聊天历史访问能力。
package share

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/storage"
)

// ChatHistoryWriter 只接收适配器归一化后的历史记录。
type ChatHistoryWriter interface {
	Append(context.Context, *storage.ChatMessage) error
}

// RecordAssistantMessage 写入单条已提交的平台消息；记录失败不改变发送结果。
func RecordAssistantMessage(ctx context.Context, w ChatHistoryWriter, m storage.ChatMessage, segments []storage.ChatSegment) {
	if ctx.Value(notificationSendKey{}) != nil {
		return
	}
	if len(segments) == 1 && segments[0].Type == "text" {
		m.Text = segments[0].Text
	} else if len(segments) > 0 {
		var text strings.Builder
		for _, s := range segments {
			text.WriteString(s.Text)
		}
		m.Text = text.String()
		m.Segments = storage.MarshalChatSegments(segments)
	}
	m.IsAssistant = true
	appendHistory(context.WithoutCancel(ctx), w, &m)
}

// RecordInboundMessage 将三个平台归一化的入站片段和引用快照写入历史。
func RecordInboundMessage(ctx context.Context, w ChatHistoryWriter, m storage.ChatMessage, parts []MessageSegment, reply ReplyContext) {
	segments := make([]storage.ChatSegment, len(parts))
	for i, s := range parts {
		segments[i] = storage.ChatSegment{Type: string(s.Type), Text: s.Text, UserID: s.UserID, URL: s.URL, MediaID: s.MediaID, PlatformFileID: s.PlatformFileID, MIMEType: s.MIMEType, Name: s.Name, Size: s.Size}
	}
	m.Segments = storage.MarshalChatSegments(segments)
	m.Metadata = storage.MarshalChatMetadata(storage.ChatReply{MessageID: reply.MessageID, SenderID: reply.SenderID, SenderName: reply.SenderName, Text: reply.Text, Display: reply.Display})
	appendHistory(ctx, w, &m)
}

func appendHistory(ctx context.Context, w ChatHistoryWriter, m *storage.ChatMessage) {
	if err := w.Append(ctx, m); err != nil {
		events.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError, EventName: "record_chat_message_failed", Module: m.Platform, Content: fmt.Sprintf("record chat message %s: %v", m.PlatformMessageID, err), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)})
	}
}

// ChatHistoryReader 只按平台消息身份查询单条历史。
type ChatHistoryReader interface {
	GetByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.ChatMessage, error)
}
