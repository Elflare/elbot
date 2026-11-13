// 将历史存储查询适配为 chat_history 内部契约，并转换消息与错误。
// 历史媒体获取统一委托 Tool 媒体层，不向 chat_history 暴露外部记录或下载能力。
package tool

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"errors"
	"strings"

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
		GetByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.ChatMessage, error)
	} // 借用历史查询能力。
	platform interface {
		ResolveMedia(context.Context, string, media.PlatformMedia, int64) (operationresult.Result[delivery.Source], error)
	} // 借用目标平台媒体解析能力，不取得适配器。
	media *toolmedia.MediaRuntime // 历史媒体的关联查询与获取统一经过 Tool 媒体层。
}

func (b *chatHistoryBridge) Search(ctx context.Context, query chat_history.SearchQuery) (operationresult.Result[[]share.Message], error) {
	rows, err := b.history.Search(ctx, storage.ChatHistorySearchRequest{
		Platform: query.Platform, ChatID: query.ChatID,
		QueryTerms: query.QueryTerms, QueryMode: query.QueryMode,
		SenderQuery: query.SenderQuery,
		Since:       query.Since, Until: query.Until, Limit: query.Limit, ChatType: query.ChatType,
	})
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[[]share.Message]{Status: operationresult.NotFound, Message: "当前聊天没有该消息。"}, nil
	}
	if err != nil {
		return operationresult.Result[[]share.Message]{}, err
	}
	messages, err := b.messages(ctx, rows)
	if err != nil {
		return operationresult.Result[[]share.Message]{}, err
	}
	return operationresult.Result[[]share.Message]{Status: operationresult.Success, Data: messages}, nil
}

func (b *chatHistoryBridge) Around(ctx context.Context, query chat_history.AroundQuery) (operationresult.Result[[]share.Message], error) {
	rows, err := b.history.Around(ctx, storage.ChatHistoryAroundRequest{
		Platform: query.Key.Platform, ChatID: query.Key.ChatID,
		PlatformMessageID: query.Key.MessageID, Before: query.Before, After: query.After, ChatType: query.Key.ChatType,
	})
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[[]share.Message]{Status: operationresult.NotFound, Message: "当前聊天没有该消息。"}, nil
	}
	if err != nil {
		return operationresult.Result[[]share.Message]{}, err
	}
	messages, err := b.messages(ctx, rows)
	if err != nil {
		return operationresult.Result[[]share.Message]{}, err
	}
	return operationresult.Result[[]share.Message]{Status: operationresult.Success, Data: messages}, nil
}

func (b *chatHistoryBridge) GetMessage(ctx context.Context, key share.MessageKey) (operationresult.Result[share.Message], error) {
	row, err := b.history.GetByPlatformMessage(ctx, key.Platform, key.ChatType, key.ChatID, key.MessageID)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[share.Message]{Status: operationresult.NotFound, Message: "当前聊天没有该消息。"}, nil
	}
	if err != nil {
		return operationresult.Result[share.Message]{}, err
	}
	message, err := b.message(ctx, *row, false)
	if err != nil {
		return operationresult.Result[share.Message]{}, err
	}
	return operationresult.Result[share.Message]{Status: operationresult.Success, Data: message}, nil
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
		Key:      share.MessageKey{Platform: row.Platform, ChatID: row.ChatID, MessageID: row.PlatformMessageID, ChatType: row.ChatType},
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
			entry.row, entry.err = b.history.GetByPlatformMessage(ctx, key.Platform, key.ChatType, key.ChatID, key.MessageID)
			known[key] = entry
		}
		if errors.Is(entry.err, storage.ErrNotFound) {
			results[i].Media = operationresult.Result[share.MediaInfo]{Status: operationresult.NotFound, Message: "当前聊天没有该消息。"}
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
		return nil, err
	}
	for i, item := range fetched {
		results[positions[i]] = item
	}
	return results, nil
}

func historyMediaMessage(row storage.ChatMessage) media.HistoryMessage {
	message := media.HistoryMessage{ID: row.ID, Platform: row.Platform, ChatID: row.ChatID, MessageID: row.PlatformMessageID, ChatType: row.ChatType}
	for _, segment := range storage.UnmarshalChatSegments(row.Segments) {
		if segment.Type == "image" || segment.Type == "file" {
			message.Media = append(message.Media, media.PlatformMedia{Type: segment.Type, MediaID: segment.MediaID, URL: segment.URL, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size})
		}
	}
	return message
}

// resolveHistoryChat 只在 Tool 边界解释调用范围；查询组件取得已确定的地址。
func resolveHistoryChat(ctx context.Context, req share.SearchRequest) (operationresult.Result[contextinfo.Source], error) {
	if share.PolicyFromContext(ctx).ExplicitChat {
		if req.Platform == nil || req.ChatType == nil || req.ChatID == nil || strings.TrimSpace(*req.Platform) == "" || *req.ChatType == "" || *req.ChatID == "" {
			return operationresult.Result[contextinfo.Source]{Status: operationresult.InvalidArguments, Message: "无聊天上下文时，查询聊天历史必须指定非空的 platform、chat_type 和 chat_id。"}, nil
		}
		return operationresult.Result[contextinfo.Source]{Status: operationresult.Success, Data: contextinfo.Source{Platform: *req.Platform, ChatType: *req.ChatType, ChatID: *req.ChatID}}, nil
	}
	if req.Platform != nil || req.ChatType != nil || req.ChatID != nil {
		return operationresult.Result[contextinfo.Source]{Status: operationresult.InvalidArguments, Message: "已有聊天上下文时，查询聊天历史不能指定 platform、chat_type 或 chat_id，请使用当前聊天。"}, nil
	}
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok {
		return operationresult.Result[contextinfo.Source]{Status: operationresult.NoCurrentSession, Message: "当前上下文没有平台聊天信息，无法自动确定要查询哪个聊天。"}, nil
	}
	return operationresult.Result[contextinfo.Source]{Status: operationresult.Success, Data: info.Source}, nil
}
