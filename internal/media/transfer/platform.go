package transfer

import (
	"context"
	"errors"
	"fmt"
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
	ID        string          // 历史记录身份。
	Platform  string          // 所属平台。
	ScopeID   string          // 聊天范围。
	MessageID string          // 平台消息标识。
	Media     []PlatformMedia // 有序媒体来源，下标加一为媒体序号。
}

// HistoryMediaRequest 选择一个历史媒体位置。
type HistoryMediaRequest struct {
	Message HistoryMessage // 历史身份和媒体来源。
	Index   int            // 从 1 开始的媒体序号。
}

// HistoryMediaResult 保存逐位置的结果或错误。
type HistoryMediaResult struct {
	Media share.MediaInfo // 获取到的通用信息；先检查 Err。
	Err   error           // 当前位置的获取或关联错误。
}

// HistoryFetchOptions 限制未缓存位置的获取次数。
type HistoryFetchOptions struct {
	MaxFetchAttempts int // 包含失败尝试，非正数不限；缓存不占额度。
}

// ErrHistoryFetchLimit 表示获取额度耗尽，根部公开同一个错误值。
var ErrHistoryFetchLimit = errors.New("history media fetch limit reached")

// ErrHistoryMediaIndex 表示媒体序号越界，根部公开同一个错误值。
var ErrHistoryMediaIndex = errors.New("history media index out of range")

// ImportPlatform 解析平台来源并交给对象组件导入，不持久化传输凭据或路径。
// 已有媒体标识直接查询信息，解析函数接收配置限定的最大字节数。
func (m *Manager) ImportPlatform(ctx context.Context, platformName string, resolver func(context.Context, PlatformMedia, int64) (delivery.Source, error), segment PlatformMedia) (share.MediaInfo, error) {
	if segment.MediaID != "" {
		return m.objects.Info(ctx, segment.MediaID)
	}
	if err := ctx.Err(); err != nil {
		return share.MediaInfo{}, err
	}
	if segment.Size > m.options.MaxImportBytes {
		return share.MediaInfo{}, fmt.Errorf("media exceeds import limit of %d bytes", m.options.MaxImportBytes)
	}
	source := delivery.Source{URL: strings.TrimSpace(segment.URL)}
	var err error
	if source.URL == "" && resolver != nil {
		source, err = resolver(ctx, segment, m.options.MaxImportBytes)
	}
	if err != nil {
		return share.MediaInfo{}, err
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
		return share.MediaInfo{}, fmt.Errorf("platform media source unavailable")
	}
}

// GetHistoryMediaBatch 查询对象关联及缓存，仅在未命中时解析平台来源。
// 同次调用复用重复位置，保持输入顺序、逐项错误及未缓存获取额度。
// 对象查询错误或取消中止本次批量；成功结果按需建立持久历史关联。
func (m *Manager) GetHistoryMediaBatch(ctx context.Context, requests []HistoryMediaRequest, resolver func(context.Context, string, PlatformMedia, int64) (delivery.Source, error), opts HistoryFetchOptions) ([]HistoryMediaResult, error) {
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
		key := share.HistoryKey{ID: row.ID, Platform: row.Platform, ScopeID: row.ScopeID, MessageID: row.MessageID}
		pos := position{key, index}
		if result, ok := seen[pos]; ok {
			results = append(results, result)
			continue
		}
		var result HistoryMediaResult
		if index < 1 || index > len(row.Media) {
			result.Err = fmt.Errorf("%w: %d (message has %d media)", ErrHistoryMediaIndex, index, len(row.Media))
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
				result.Media = entry.Media
			} else if opts.MaxFetchAttempts > 0 && attempts >= opts.MaxFetchAttempts {
				result.Err = ErrHistoryFetchLimit
			} else {
				attempts++
				var resolve func(context.Context, PlatformMedia, int64) (delivery.Source, error)
				if resolver != nil {
					resolve = func(ctx context.Context, input PlatformMedia, limit int64) (delivery.Source, error) {
						return resolver(ctx, row.Platform, input, limit)
					}
				}
				result.Media, result.Err = m.ImportPlatform(ctx, row.Platform, resolve, segment)
			}
			if result.Err == nil && (!entry.Associated || entry.Kind != segment.Type) {
				result.Err = m.objects.AssociateHistory(ctx, key, index, segment.Type, result.Media.ID)
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(result.Err, err)
		}
		seen[pos] = result
		results = append(results, result)
	}
	return results, ctx.Err()
}
