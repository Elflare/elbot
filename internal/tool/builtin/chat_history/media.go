package chat_history

import (
	"context"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	toolshare "elbot/internal/tool/share"
	"fmt"
)

type MediaRequest struct {
	Seqs       []int64 // 保留用户指定的消息顺序和重复项。
	MediaIndex [][]int // nil 使用每条消息的默认媒体；非 nil 时与消息逐项对应且每项非空。
}

// validateMediaIndex checks the optional per-message selection before any lookup or download.
func validateMediaIndex(seqs []int64, indexes [][]int) error {
	if indexes == nil {
		return nil
	}
	if len(indexes) != len(seqs) {
		return fmt.Errorf("media_index 必须与 message_id 逐项对应")
	}
	for _, items := range indexes {
		if len(items) == 0 {
			return fmt.Errorf("media_index 每项不能为空")
		}
		for _, i := range items {
			if i < 1 {
				return fmt.Errorf("媒体序号从 1 开始")
			}
		}
	}
	return nil
}

type MediaResult struct {
	Seq     int64                                       // 请求对应的历史序号。
	Index   int                                         // 对应媒体序号；消息不存在时为 0。
	IsImage bool                                        // 成功选中的媒体是否为图片。
	Media   operationresult.Result[toolshare.MediaInfo] // 单项业务结果，成功时读取媒体信息。
	Err     error                                       // 此项消息查询或媒体获取的真实故障。
}

func (m Deps) getMedia(ctx context.Context, req MediaRequest, imagesOnly bool) (operationresult.Result[[]MediaResult], error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return operationresult.Result[[]MediaResult]{}, err
	}
	if chat.Status != operationresult.Success {
		return operationresult.Result[[]MediaResult]{Status: chat.Status, Message: chat.Message}, nil
	}
	known := make(map[int64]operationresult.Result[media.HistoryMessage])
	var results []MediaResult
	var queries []media.HistoryMediaRequest
	var positions []int
	for i, id := range req.Seqs {
		if err := ctx.Err(); err != nil {
			return operationresult.Result[[]MediaResult]{}, err
		}
		entry, ok := known[id]
		if !ok {
			row, err := m.getMessage(ctx, chat.Data, id, false)
			if err != nil {
				return operationresult.Result[[]MediaResult]{}, err
			}
			entry.Status, entry.Message = row.Status, row.Message
			if row.Status == operationresult.Success {
				entry.Data = historyMediaMessage(*row.Data)
			}
			known[id] = entry
		}
		if entry.Status != operationresult.Success {
			if imagesOnly {
				return operationresult.Result[[]MediaResult]{Status: entry.Status, Message: entry.Message}, nil
			}
			results = append(results, MediaResult{Seq: id, Media: operationresult.Result[toolshare.MediaInfo]{Status: entry.Status, Message: entry.Message}})
			continue
		}
		message := entry.Data
		indexes := []int{1}
		var images []int
		if imagesOnly {
			for j, item := range message.Media {
				if item.Type == "image" {
					images = append(images, j+1)
				}
			}
			if len(images) == 0 {
				return operationresult.Result[[]MediaResult]{Status: operationresult.NotFound, Message: fmt.Sprintf("[h%d] 没有图片", id)}, nil
			}
			indexes = images[:1]
		}
		if req.MediaIndex != nil {
			indexes = req.MediaIndex[i]
		}
		for _, index := range indexes {
			result := MediaResult{Seq: id, Index: index, Media: operationresult.Result[toolshare.MediaInfo]{Status: operationresult.Success}}
			if index > len(message.Media) {
				result.Media = operationresult.Result[toolshare.MediaInfo]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("[h%d] 媒体序号 %d 越界，消息共有 %d 项媒体。", id, index, len(message.Media))}
			} else {
				result.IsImage = message.Media[index-1].Type == "image"
			}
			if imagesOnly && (result.Media.Status != operationresult.Success || !result.IsImage) {
				return operationresult.Result[[]MediaResult]{Status: operationresult.InvalidTarget, Message: fmt.Sprintf("[h%d] 序号 %d 不是图片；可选图片序号：%v", id, index, images)}, nil
			}
			if result.Media.Status == operationresult.Success {
				positions = append(positions, len(results))
				queries = append(queries, media.HistoryMediaRequest{Message: message, Index: index})
			}
			results = append(results, result)
		}
	}
	if len(queries) == 0 {
		return operationresult.Result[[]MediaResult]{Status: operationresult.Success, Data: results}, nil
	}
	fetched, err := m.Media.GetHistoryMediaBatch(ctx, queries, m.ResolveMedia, media.HistoryFetchOptions{MaxFetchAttempts: 5})
	if err != nil {
		return operationresult.Result[[]MediaResult]{}, err
	}
	for i, item := range fetched {
		result := &results[positions[i]]
		result.Media, result.Err = item.Media, item.Err
	}
	return operationresult.Result[[]MediaResult]{Status: operationresult.Success, Data: results}, nil
}
