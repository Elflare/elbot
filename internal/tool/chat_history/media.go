// 定义历史媒体请求与结果，处理媒体序号、图片选择、结果顺序及单次获取上限。
// 通过注入的内部能力协调媒体获取，不接收平台下载器或存储记录。
package chat_history

import (
	"context"
	"elbot/internal/operationresult"
	"fmt"

	"elbot/internal/tool/share"
)

// share.MaxFetchAttempts 限制一次工具调用中未缓存媒体的获取尝试，包括失败；缓存和重复项不占次数。

// share.MediaRequest 由工具完成参数形状校验：消息 ID 非空且已规范化，序号为正数。

type MediaQuery struct {
	Key   share.MessageKey // 媒体所属消息的查询位置。
	Index int              // 从 1 开始的媒体序号。
}

func (m *Manager) GetMedia(ctx context.Context, req share.MediaRequest) (operationresult.Result[[]share.MediaResult], error) {
	return m.getMedia(ctx, req, false)
}

func (m *Manager) GetImages(ctx context.Context, req share.MediaRequest) (operationresult.Result[[]share.MediaResult], error) {
	return m.getMedia(ctx, req, true)
}

func (m *Manager) getMedia(ctx context.Context, req share.MediaRequest, imagesOnly bool) (operationresult.Result[[]share.MediaResult], error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return operationresult.Result[[]share.MediaResult]{}, err
	}
	if chat.Status != operationresult.Success {
		return operationresult.Result[[]share.MediaResult]{Status: chat.Status, Message: chat.Message}, nil
	}
	type lookup struct {
		message operationresult.Result[share.Message]
		err     error
	}
	known := make(map[string]lookup)
	var results []share.MediaResult
	var queries []MediaQuery
	var positions []int
	for i, id := range req.MessageIDs {
		if err := ctx.Err(); err != nil {
			return operationresult.Result[[]share.MediaResult]{}, err
		}
		entry, ok := known[id]
		if !ok {
			key := share.MessageKey{Platform: chat.Data.Platform, ChatID: chat.Data.ChatID, MessageID: id, ChatType: chat.Data.ChatType}
			entry.message, entry.err = m.backend.GetMessage(ctx, key)
			known[id] = entry
		}
		if entry.err != nil {
			return operationresult.Result[[]share.MediaResult]{}, entry.err
		}
		if entry.message.Status != operationresult.Success {
			if imagesOnly {
				return operationresult.Result[[]share.MediaResult]{Status: entry.message.Status, Message: entry.message.Message}, nil
			}
			results = append(results, share.MediaResult{MessageID: id, Media: operationresult.Result[share.MediaInfo]{Status: entry.message.Status, Message: entry.message.Message}})
			continue
		}
		message := entry.message.Data
		indexes := []int{1}
		var images []int
		if imagesOnly {
			for j, item := range message.Media {
				if item.IsImage {
					images = append(images, j+1)
				}
			}
			if len(images) == 0 {
				return operationresult.Result[[]share.MediaResult]{Status: operationresult.NotFound, Message: fmt.Sprintf("[#%s] 没有图片", id)}, nil
			}
			indexes = images[:1]
		}
		if req.MediaIndex != nil {
			indexes = req.MediaIndex[i]
		}
		for _, index := range indexes {
			result := share.MediaResult{MessageID: id, Index: index, Media: operationresult.Result[share.MediaInfo]{Status: operationresult.Success}}
			if index > len(message.Media) {
				result.Media = operationresult.Result[share.MediaInfo]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("[#%s] 媒体序号 %d 越界，消息共有 %d 项媒体。", id, index, len(message.Media))}
			} else {
				result.IsImage = message.Media[index-1].IsImage
			}
			if imagesOnly && (result.Media.Status != operationresult.Success || !result.IsImage) {
				return operationresult.Result[[]share.MediaResult]{Status: operationresult.InvalidTarget, Message: fmt.Sprintf("[#%s] 序号 %d 不是图片；可选图片序号：%v", id, index, images)}, nil
			}
			if result.Media.Status == operationresult.Success {
				positions = append(positions, len(results))
				queries = append(queries, MediaQuery{Key: message.Key, Index: index})
			}
			results = append(results, result)
		}
	}
	if len(queries) == 0 {
		return operationresult.Result[[]share.MediaResult]{Status: operationresult.Success, Data: results}, nil
	}
	fetched, err := m.backend.FetchMedia(ctx, queries, share.MaxFetchAttempts)
	if err != nil {
		return operationresult.Result[[]share.MediaResult]{}, err
	}
	for i, item := range fetched {
		result := &results[positions[i]]
		result.Media, result.Err = item.Media, item.Err
	}
	return operationresult.Result[[]share.MediaResult]{Status: operationresult.Success, Data: results}, nil
}
