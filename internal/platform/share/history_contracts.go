// history_contracts.go 定义平台内部共用的最小聊天历史访问能力。
package share

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

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

// InboundMessage 只携带当前消息的历史事实，正文来自有序片段。
type InboundMessage struct {
	contextinfo.Conversation
	Segments             []MessageSegment
	CreatedAt            time.Time
	Raw, SenderAvatarURL string
}

// RecordInboundMessage 将当前消息和已解析的引用快照一次写入历史。
func RecordInboundMessage(ctx context.Context, w ChatHistoryWriter, m InboundMessage, reply ReplyContext) {
	if m.PlatformMessageID == "" || len(m.Segments) == 0 {
		return
	}
	var text strings.Builder
	segments := make([]storage.ChatSegment, len(m.Segments))
	for i, s := range m.Segments {
		if s.Type == SegmentText || s.Type == SegmentAt {
			text.WriteString(s.Text)
		}
		segments[i] = storage.ChatSegment{Type: string(s.Type), Text: s.Text, UserID: s.UserID, URL: s.URL, MediaID: s.MediaID, PlatformFileID: s.PlatformFileID, MIMEType: s.MIMEType, Name: s.Name, Size: s.Size}
	}
	appendHistory(ctx, w, &storage.ChatMessage{
		Platform: m.Source.Platform, ChatType: m.Source.ChatType, ChatID: m.Source.ChatID, PlatformMessageID: m.PlatformMessageID,
		SenderID: m.Identity.ID, SenderName: m.Identity.Name, SenderAvatarURL: m.SenderAvatarURL, CreatedAt: m.CreatedAt,
		Text: text.String(), Raw: m.Raw, Segments: storage.MarshalChatSegments(segments), ReplyToPlatformMessageID: m.ReplyToMessageID,
		Metadata: storage.MarshalChatMetadata(storage.ChatReply{SenderID: reply.SenderID, SenderName: reply.SenderName, Text: reply.Text, Display: reply.Display}),
	})
}

func appendHistory(ctx context.Context, w ChatHistoryWriter, m *storage.ChatMessage) {
	if err := w.Append(ctx, m); err != nil {
		events.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError, EventName: "record_chat_message_failed", Module: m.Platform, Content: fmt.Sprintf("record chat message %s: %v", m.PlatformMessageID, err), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)})
	}
}
