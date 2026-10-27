// 将历史存储查询适配为 chat_history 内部契约，并转换消息与错误。
// 历史媒体获取统一委托 Tool 媒体层，不向 chat_history 暴露外部记录或下载能力。
package tool

import (
	"context"
	"elbot/internal/delivery"
	"errors"
	"fmt"

	"elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/tool/chat_history"
	toolmedia "elbot/internal/tool/media"
	"elbot/internal/tool/share"
)

type chatHistoryBridge struct {
	history interface {
		Search(context.Context, storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error)
		Around(context.Context, storage.ChatHistoryAroundRequest) ([]storage.ChatMessage, error)
		GetByPlatformMessage(context.Context, string, string, string) (*storage.ChatMessage, error)
	} // 借用历史查询能力。
	platform interface {
		ResolveMedia(context.Context, string, media.PlatformMedia, int64) (delivery.Source, error)
	} // 借用目标平台媒体解析能力，不取得适配器。
	media *toolmedia.MediaRuntime // 历史媒体的关联查询与获取统一经过 Tool 媒体层。
}

func (b *chatHistoryBridge) Search(ctx context.Context, query chat_history.SearchQuery) ([]share.Message, error) {
	rows, err := b.history.Search(ctx, storage.ChatHistorySearchRequest{
		Platform: query.Platform, PlatformScopeID: query.ScopeID,
		QueryTerms: query.QueryTerms, QueryMode: query.QueryMode,
		SenderQuery: query.SenderQuery,
		Since:       query.Since, Until: query.Until, Limit: query.Limit,
	})
	if err != nil {
		return nil, historyError(err)
	}
	return b.messages(ctx, rows)
}

func (b *chatHistoryBridge) Around(ctx context.Context, query chat_history.AroundQuery) ([]share.Message, error) {
	rows, err := b.history.Around(ctx, storage.ChatHistoryAroundRequest{
		Platform: query.Key.Platform, PlatformScopeID: query.Key.ScopeID,
		PlatformMessageID: query.Key.MessageID, Before: query.Before, After: query.After,
	})
	if err != nil {
		return nil, historyError(err)
	}
	return b.messages(ctx, rows)
}

func (b *chatHistoryBridge) GetMessage(ctx context.Context, key share.MessageKey) (share.Message, error) {
	row, err := b.history.GetByPlatformMessage(ctx, key.Platform, key.ScopeID, key.MessageID)
	if err != nil {
		return share.Message{}, historyError(err)
	}
	return b.message(ctx, *row, false)
}

func (b *chatHistoryBridge) messages(ctx context.Context, rows []storage.ChatMessage) ([]share.Message, error) {
	result := make([]share.Message, len(rows))
	for i, row := range rows {
		message, err := b.message(ctx, row, true)
		if err != nil {
			return nil, err
		}
		result[i] = message
	}
	return result, nil
}

func (b *chatHistoryBridge) message(ctx context.Context, row storage.ChatMessage, includeCached bool) (share.Message, error) {
	reply := storage.ChatMessageReply(row)
	message := share.Message{
		Key:      share.MessageKey{Platform: row.Platform, ScopeID: row.PlatformScopeID, MessageID: row.PlatformMessageID},
		SenderID: row.SenderID, SenderName: row.SenderName, Text: row.Text, CreatedAt: row.CreatedAt,
		Reply: share.ReplyContext{MessageID: reply.MessageID, SenderID: reply.SenderID, SenderName: reply.SenderName, Text: reply.Text},
	}
	history := historyMediaMessage(row)
	segments := history.Media
	if len(segments) == 0 {
		return message, nil
	}
	var ids map[int]string
	if includeCached {
		var err error
		ids, err = b.media.HistoryIDs(ctx, history)
		if err != nil {
			return share.Message{}, err
		}
	}
	message.Media = make([]share.MediaItem, len(segments))
	for i, segment := range segments {
		message.Media[i] = share.MediaItem{IsImage: segment.Type == "image", MediaID: ids[i+1]}
	}
	return message, nil
}

// FetchMedia 按消息键恢复外部获取参数；原始记录只在本次适配调用内暂存。
func (b *chatHistoryBridge) FetchMedia(ctx context.Context, queries []chat_history.MediaQuery, maxFetchAttempts int) ([]share.HistoryMediaResult, error) {
	type lookup struct {
		row *storage.ChatMessage
		err error
	}
	known := make(map[share.MessageKey]lookup)
	results := make([]share.HistoryMediaResult, len(queries))
	var requests []media.HistoryMediaRequest
	var positions []int
	for i, query := range queries {
		entry, ok := known[query.Key]
		if !ok {
			key := query.Key
			entry.row, entry.err = b.history.GetByPlatformMessage(ctx, key.Platform, key.ScopeID, key.MessageID)
			known[key] = entry
		}
		if errors.Is(entry.err, storage.ErrNotFound) {
			results[i].Err = historyError(entry.err)
			continue
		}
		if entry.err != nil {
			return nil, entry.err
		}
		requests = append(requests, media.HistoryMediaRequest{Message: historyMediaMessage(*entry.row), Index: query.Index})
		positions = append(positions, i)
	}
	if len(requests) == 0 {
		return results, nil
	}
	fetched, err := b.media.GetHistoryMediaBatch(ctx, requests, b.platform.ResolveMedia, media.HistoryFetchOptions{MaxFetchAttempts: maxFetchAttempts})
	if err != nil {
		return nil, historyError(err)
	}
	for i, item := range fetched {
		item.Err = historyError(item.Err)
		results[positions[i]] = item
	}
	return results, nil
}

func historyError(err error) error {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("%w: %v", share.ErrNotFound, err)
	case errors.Is(err, media.ErrHistoryMediaIndex):
		return fmt.Errorf("%w: %v", share.ErrMediaIndex, err)
	case errors.Is(err, media.ErrHistoryFetchLimit):
		return fmt.Errorf("%w: %v", share.ErrFetchLimit, err)
	default:
		return err
	}
}

func historyMediaMessage(row storage.ChatMessage) media.HistoryMessage {
	message := media.HistoryMessage{ID: row.ID, Platform: row.Platform, ScopeID: row.PlatformScopeID, MessageID: row.PlatformMessageID}
	for _, segment := range storage.UnmarshalChatSegments(row.Segments) {
		if segment.Type == "image" || segment.Type == "file" {
			message.Media = append(message.Media, media.PlatformMedia{Type: segment.Type, MediaID: segment.MediaID, URL: segment.URL, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size})
		}
	}
	return message
}
