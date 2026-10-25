// 定义历史媒体请求与结果，处理媒体序号、图片选择、结果顺序及单次获取上限。
// 通过注入的内部能力协调媒体获取，不接收平台下载器或存储记录。
package chat_history

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/tool/share"
)

// MaxFetchAttempts 限制一次工具调用中未缓存媒体的获取尝试，包括失败；缓存和重复项不占次数。
const MaxFetchAttempts = 5

var (
	ErrMediaIndex = errors.New("history media index out of range")
	ErrFetchLimit = errors.New("history media fetch limit reached")
)

type MediaItem struct {
	IsImage bool   // 此媒体位置是否为图片，其余类型按文件展示。
	MediaID string // 已有的媒体引用；未获取时为空。
}

// MediaRequest 由工具完成参数形状校验：消息 ID 非空且已规范化，序号为正数。
type MediaRequest struct {
	MessageIDs []string // 保留用户指定的消息顺序和重复项。
	MediaIndex [][]int  // nil 使用每条消息的默认媒体；非 nil 时与消息逐项对应且每项非空。
}

type MediaQuery struct {
	Key   MessageKey // 媒体所属消息的查询位置。
	Index int        // 从 1 开始的媒体序号。
}

type MediaResult struct {
	MessageID string          // 请求对应的消息 ID。
	Index     int             // 对应媒体序号；消息不存在时为 0。
	IsImage   bool            // 成功选中的媒体是否为图片。
	Media     share.MediaInfo // 获取成功后的通用信息。
	Err       error           // 此项的消息、序号或获取错误。
}

func (m *Manager) GetMedia(ctx context.Context, req MediaRequest) ([]MediaResult, error) {
	return m.getMedia(ctx, req, false)
}

func (m *Manager) GetImages(ctx context.Context, req MediaRequest) ([]MediaResult, error) {
	return m.getMedia(ctx, req, true)
}

func (m *Manager) getMedia(ctx context.Context, req MediaRequest, imagesOnly bool) ([]MediaResult, error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return nil, err
	}
	type lookup struct {
		message Message
		err     error
	}
	known := make(map[string]lookup)
	var results []MediaResult
	var queries []MediaQuery
	var positions []int
	for i, id := range req.MessageIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry, ok := known[id]
		if !ok {
			key := MessageKey{Platform: chat.Platform, ScopeID: chat.ScopeID, MessageID: id}
			entry.message, entry.err = m.backend.GetMessage(ctx, key)
			known[id] = entry
		}
		if errors.Is(entry.err, ErrNotFound) {
			if imagesOnly {
				return nil, fmt.Errorf("[#%s] 当前聊天没有该消息", id)
			}
			results = append(results, MediaResult{MessageID: id, Err: entry.err})
			continue
		}
		if entry.err != nil {
			return nil, entry.err
		}
		message := entry.message
		indexes := []int{1}
		var images []int
		if imagesOnly {
			for j, item := range message.Media {
				if item.IsImage {
					images = append(images, j+1)
				}
			}
			if len(images) == 0 {
				return nil, fmt.Errorf("[#%s] 没有图片", id)
			}
			indexes = images[:1]
		}
		if req.MediaIndex != nil {
			indexes = req.MediaIndex[i]
		}
		for _, index := range indexes {
			result := MediaResult{MessageID: id, Index: index}
			if index > len(message.Media) {
				result.Err = ErrMediaIndex
			} else {
				result.IsImage = message.Media[index-1].IsImage
			}
			if imagesOnly && (result.Err != nil || !result.IsImage) {
				return nil, fmt.Errorf("[#%s] 序号 %d 不是图片；可选图片序号：%v", id, index, images)
			}
			if result.Err == nil {
				positions = append(positions, len(results))
				queries = append(queries, MediaQuery{Key: message.Key, Index: index})
			}
			results = append(results, result)
		}
	}
	if len(queries) == 0 {
		return results, nil
	}
	fetched, err := m.backend.FetchMedia(ctx, queries, MaxFetchAttempts)
	if err != nil {
		return nil, err
	}
	for i, item := range fetched {
		result := &results[positions[i]]
		result.Media, result.Err = item.Media, item.Err
	}
	return results, nil
}
