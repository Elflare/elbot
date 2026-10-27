// history.go 持有平台聊天历史存储，提供对外查询和清理入口。

package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/platform/share"
	"elbot/internal/storage"
)

func (m *Supervisor) SearchChatHistory(ctx context.Context, req ChatHistorySearchRequest) ([]ChatMessage, error) {
	rows, err := m.history.Search(ctx, storage.ChatHistorySearchRequest{
		Platform: req.Platform, PlatformScopeID: req.ScopeID, QueryTerms: req.QueryTerms, QueryMode: req.QueryMode,
		SenderQuery: req.SenderQuery, Since: req.Since, Until: req.Until, Limit: req.Limit,
	})
	if err != nil {
		return nil, chatHistoryError(err)
	}
	return chatHistoryMessages(rows), nil
}

func (m *Supervisor) ChatHistoryAround(ctx context.Context, req ChatHistoryAroundRequest) ([]ChatMessage, error) {
	rows, err := m.history.Around(ctx, storage.ChatHistoryAroundRequest{
		Platform: req.Platform, PlatformScopeID: req.ScopeID, PlatformMessageID: req.MessageID, Before: req.Before, After: req.After,
	})
	if err != nil {
		return nil, chatHistoryError(err)
	}
	return chatHistoryMessages(rows), nil
}

func (m *Supervisor) GetChatHistoryMessage(ctx context.Context, platform, scopeID, messageID string) (*ChatMessage, error) {
	row, err := m.history.GetByPlatformMessage(ctx, platform, scopeID, messageID)
	if err != nil {
		return nil, chatHistoryError(err)
	}
	message := chatHistoryMessage(*row)
	return &message, nil
}

func chatHistoryMessages(rows []storage.ChatMessage) []ChatMessage {
	result := make([]ChatMessage, len(rows))
	for i, row := range rows {
		result[i] = chatHistoryMessage(row)
	}
	return result
}

func chatHistoryMessage(row storage.ChatMessage) ChatMessage {
	reply := share.ChatMessageReply(row)
	message := ChatMessage{
		ID: row.ID, Platform: row.Platform, ScopeID: row.PlatformScopeID, MessageID: row.PlatformMessageID,
		SenderID: row.SenderID, SenderName: row.SenderName, Text: row.Text, CreatedAt: row.CreatedAt,
		Reply: ReplyContext{MessageID: reply.MessageID, SenderID: reply.SenderID, SenderName: reply.SenderName, Text: reply.Text},
	}
	for _, segment := range share.UnmarshalChatSegments(row.Segments) {
		if segment.Type == share.SegmentImage || segment.Type == share.SegmentFile {
			message.Media = append(message.Media, media.PlatformMedia{Type: string(segment.Type), MediaID: segment.MediaID,
				URL: segment.URL, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size})
		}
	}
	return message
}

func chatHistoryError(err error) error {
	if errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("%w: %v", ErrChatHistoryNotFound, err)
	}
	return err
}

// ResolveMedia 使用历史消息所属平台的解析能力，不读取当前消息上下文。
func (m *Supervisor) ResolveMedia(ctx context.Context, platform string, input media.PlatformMedia, maxBytes int64) (delivery.Source, error) {
	return m.dispatcher.ResolveMedia(ctx, platform, share.MessageSegment{Type: share.MessageSegmentType(input.Type),
		MediaID: input.MediaID, URL: input.URL, PlatformFileID: input.PlatformFileID, MIMEType: input.MIMEType, Name: input.Name, Size: input.Size}, maxBytes)
}

func (m *Supervisor) CleanupChatHistory(ctx context.Context, cutoff time.Time) (int, error) {
	return m.history.DeleteBefore(ctx, cutoff)
}
