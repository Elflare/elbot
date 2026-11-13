package transfer

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/media/share"
)

// PlatformMedia 是本组件独用的平台来源，不进入对象存储契约。
type PlatformMedia struct {
	Type           string // 媒体种类。
	MediaID        string // 已导入的媒体标识。
	URL            string // 平台传输地址。
	PlatformFileID string // 平台原生文件标识。
	MIMEType       string // 已知内容类型。
	Name           string // 展示名称。
	Size           int64  // 已知字节数。
}

// HistoryMessage 包含批量获取所需的历史身份及有序来源。
type HistoryMessage struct {
	ID        string               // 历史记录身份。
	Platform  string               // 所属平台。
	ChatType  contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID    string               // 聊天范围。
	MessageID string               // 平台消息标识。
	Media     []PlatformMedia      // 有序媒体来源，下标加一为媒体序号。
}

// HistoryMediaRequest 选择一个历史媒体位置。
type HistoryMediaRequest struct {
	Message HistoryMessage // 历史身份和媒体来源。
	Index   int            // 从 1 开始的媒体序号。
}

// HistoryMediaResult 保存逐位置的结果或错误。
type HistoryMediaResult struct {
	Media operationresult.Result[share.MediaInfo] // 单项业务结果；成功时才读取通用媒体信息。
	Err   error                                   // 当前位置的真实获取或关联故障。
}

// HistoryFetchOptions 限制未缓存位置的获取次数。
type HistoryFetchOptions struct {
	MaxFetchAttempts int // 包含失败尝试，非正数不限；缓存不占额度。
}

// ImportPlatform 解析平台来源并交给对象组件导入，不持久化传输凭据或路径。
// 已有媒体标识直接查询信息，解析函数接收配置限定的最大字节数。
func (m *Manager) ImportPlatform(ctx context.Context, platformName string, resolver func(context.Context, PlatformMedia, int64) (operationresult.Result[delivery.Source], error), segment PlatformMedia) (operationresult.Result[share.MediaInfo], error) {
	if segment.MediaID != "" {
		return m.objects.Info(ctx, segment.MediaID)
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[share.MediaInfo]{}, err
	}
	if segment.Size > m.options.MaxImportBytes {
		return operationresult.Result[share.MediaInfo]{Status: operationresult.Rejected, Message: fmt.Sprintf("media exceeds import limit of %d bytes", m.options.MaxImportBytes)}, nil
	}
	source := delivery.Source{URL: strings.TrimSpace(segment.URL)}
	if source.URL == "" && resolver != nil {
		resolved, err := resolver(ctx, segment, m.options.MaxImportBytes)
		if err != nil {
			return operationresult.Result[share.MediaInfo]{}, err
		}
		if resolved.Status != operationresult.Success {
			return operationresult.Result[share.MediaInfo]{Status: resolved.Status, Message: resolved.Message}, nil
		}
		source = resolved.Data
	}
	input := share.Input{Name: segment.Name, MIMEType: segment.MIMEType, Source: share.Source{Platform: platformName, URL: segment.URL, FileID: segment.PlatformFileID}}
	if input.MIMEType == "" {
		input.MIMEType = source.MIMEType
	}
	limits := share.ImportLimits{MaxImportBytes: m.options.MaxImportBytes, DownloadTimeout: m.options.DownloadTimeout}
	switch {
	case source.MediaID != "":
		return m.objects.Info(ctx, source.MediaID)
	case source.URL != "":
		return m.objects.ImportURL(ctx, source.URL, input, limits)
	case source.Path != "":
		return m.objects.ImportFile(ctx, source.Path, input, limits)
	case source.Data != nil:
		return m.objects.ImportBytes(ctx, source.Data, input, limits)
	default:
		return operationresult.Result[share.MediaInfo]{Status: operationresult.Unavailable, Message: "platform media source unavailable"}, nil
	}
}

// GetHistoryMediaBatch 查询对象关联及缓存，仅在未命中时解析平台来源。
// 同次调用复用重复位置，保持输入顺序、逐项错误及未缓存获取额度。
// 对象查询错误或取消中止本次批量；成功结果按需建立持久历史关联。
func (m *Manager) GetHistoryMediaBatch(ctx context.Context, requests []HistoryMediaRequest, resolver func(context.Context, string, PlatformMedia, int64) (operationresult.Result[delivery.Source], error), opts HistoryFetchOptions) ([]HistoryMediaResult, error) {
	type position struct {
		key   share.HistoryKey
		index int
	}
	known := map[share.HistoryKey]map[int]share.HistoryEntry{}
	seen := map[position]HistoryMediaResult{}
	results := make([]HistoryMediaResult, 0, len(requests))
	attempts := 0
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, index := request.Message, request.Index
		key := share.HistoryKey{ID: row.ID, Platform: row.Platform, ChatID: row.ChatID, MessageID: row.MessageID, ChatType: row.ChatType}
		pos := position{key, index}
		if result, ok := seen[pos]; ok {
			results = append(results, result)
			continue
		}
		var result HistoryMediaResult
		if index < 1 || index > len(row.Media) {
			result.Media = operationresult.Result[share.MediaInfo]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("history media index %d out of range (message has %d media)", index, len(row.Media))}
		} else {
			entries, ok := known[key]
			if !ok {
				var err error
				entries, err = m.objects.LoadHistoryMedia(ctx, key)
				if err != nil {
					return nil, err
				}
				known[key] = entries
			}
			entry, segment := entries[index], row.Media[index-1]
			if entry.Media.ID != "" {
				result.Media = operationresult.Result[share.MediaInfo]{Status: operationresult.Success, Data: entry.Media}
			} else if opts.MaxFetchAttempts > 0 && attempts >= opts.MaxFetchAttempts {
				result.Media = operationresult.Result[share.MediaInfo]{Status: operationresult.Rejected, Message: fmt.Sprintf("history media fetch limit reached (%d attempts)", opts.MaxFetchAttempts)}
			} else {
				attempts++
				var resolve func(context.Context, PlatformMedia, int64) (operationresult.Result[delivery.Source], error)
				if resolver != nil {
					resolve = func(ctx context.Context, input PlatformMedia, limit int64) (operationresult.Result[delivery.Source], error) {
						return resolver(ctx, row.Platform, input, limit)
					}
				}
				result.Media, result.Err = m.ImportPlatform(ctx, row.Platform, resolve, segment)
			}
			if result.Err == nil && result.Media.Status == operationresult.Success && (!entry.Associated || entry.Kind != segment.Type) {
				result.Err = m.objects.AssociateHistory(ctx, key, index, segment.Type, result.Media.Data.ID)
			}
		}
		if err := ctx.Err(); err != nil {
			if result.Err != nil && !errors.Is(result.Err, err) {
				m.options.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "history_media_failed", Module: "media", Content: result.Err.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(result.Err)})
			}
			return nil, err
		}
		seen[pos] = result
		results = append(results, result)
	}
	return results, ctx.Err()
}
