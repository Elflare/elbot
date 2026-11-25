package media

import (
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/signal"
	"io"
	"sync/atomic"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media/object"
	"elbot/internal/media/share"
	"elbot/internal/media/transfer"
)

// MediaCenter 持有内部组件，提供公开业务入口；不公开内部仓储或后端。
type MediaCenter struct {
	closed        atomic.Bool                            // 关闭后停止接纳新的媒体使用，已结束事实仍可登记。
	objects       *object.Manager                        // 对象、引用、历史关联与回收。
	transfer      *transfer.Manager                      // 入站、模型请求和平台交付。
	chatHistory   History                                // 构造时借用的历史身份查询，对账和清理时传给对象管理。
	usageFinished *signal.Signal[[]string, signal.Empty] // 本实例的使用结束通知，携带自有的使用 ID 切片。
}

func mediaInfoResult(info operationresult.Result[share.MediaInfo], err error) (operationresult.Result[MediaInfo], error) {
	if err != nil {
		return operationresult.Result[MediaInfo]{}, err
	}
	if info.Status != operationresult.Success {
		return operationresult.Result[MediaInfo]{Status: info.Status, Message: info.Message}, nil
	}
	return operationresult.Result[MediaInfo]{Status: operationresult.Success, Data: MediaInfo(info.Data)}, nil
}

// Info 查询通用信息，不读内容、不校验图片、不增加持有。
func (m *MediaCenter) Info(ctx context.Context, id string) (operationresult.Result[MediaInfo], error) {
	return mediaInfoResult(m.objects.Info(ctx, id))
}

// ValidateImage 验证图片可解码且 MIME 属于图片，返回通用信息。
// 自有持有释放失败时保留错误，并由 Cleanup 重试释放。
func (m *MediaCenter) ValidateImage(ctx context.Context, id string) (operationresult.Result[MediaInfo], error) {
	return mediaInfoResult(m.objects.ValidateImage(ctx, id))
}

func (input Input) shared() share.Input {
	return share.Input{Name: input.Name, MIMEType: input.MIMEType, Source: share.Source(input.Source)}
}

// ImportSource 读取已有 ID 的元数据，或归档调用方允许的来源。
func (m *MediaCenter) ImportSource(ctx context.Context, source delivery.Source, input Input, limits ImportLimits) (operationresult.Result[MediaInfo], error) {
	return mediaInfoResult(m.transfer.ImportSource(ctx, source, input.shared(), share.ImportLimits(limits)))
}

// PrepareOutputs 成功时返回整批的使用 ID，发送与回执处理结束后由调用方发布。
func (m *MediaCenter) PrepareOutputs(ctx context.Context, requests []OutputRequest) (operationresult.Result[[]PreparedOutput], UsageID, error) {
	if m.closed.Load() {
		return operationresult.Result[[]PreparedOutput]{Status: operationresult.Unavailable, Message: "media center is closed"}, "", nil
	}
	result, id, err := m.transfer.PrepareOutputs(ctx, requests)
	return result, UsageID(id), err
}

// ImportBytes 归档字节内容并返回通用信息。
func (m *MediaCenter) ImportBytes(ctx context.Context, data []byte, input Input, limits ImportLimits) (operationresult.Result[MediaInfo], error) {
	return mediaInfoResult(m.objects.ImportBytes(ctx, data, input.shared(), share.ImportLimits(limits)))
}

// ImportURL 按配置下载、归档内容并清理持久化来源中的敏感信息。
func (m *MediaCenter) ImportURL(ctx context.Context, rawURL string, input Input, limits ImportLimits) (operationresult.Result[MediaInfo], error) {
	return mediaInfoResult(m.objects.ImportURL(ctx, rawURL, input.shared(), share.ImportLimits(limits)))
}

// ImportFile 归档调用方已按其路径边界解析的普通文件。
func (m *MediaCenter) ImportFile(ctx context.Context, path string, input Input, limits ImportLimits) (operationresult.Result[MediaInfo], error) {
	return mediaInfoResult(m.objects.ImportFile(ctx, path, input.shared(), share.ImportLimits(limits)))
}

// ImportReader 读取受限内容并归档，size 是调用方已知的长度。
func (m *MediaCenter) ImportReader(ctx context.Context, input io.Reader, size int64, spec Input, limits ImportLimits) (operationresult.Result[MediaInfo], error) {
	return mediaInfoResult(m.objects.ImportReader(ctx, input, size, spec.shared(), share.ImportLimits(limits)))
}

// AcquireForLLM 准备请求副本；实际请求结束后发布对应使用 ID。
func (m *MediaCenter) AcquireForLLM(ctx context.Context, messages []llm.LLMMessage) (operationresult.Result[[]llm.LLMMessage], UsageID, error) {
	if m.closed.Load() {
		return operationresult.Result[[]llm.LLMMessage]{}, "", signal.ErrClosed
	}
	prepared, id, err := m.transfer.AcquireForLLM(ctx, messages)
	return prepared, UsageID(id), err
}

// Workspace 为调用创建私有目录，随使用结束交由 Media 清理。
func (m *MediaCenter) Workspace(base string) (string, UsageID, error) {
	if m.closed.Load() {
		return "", "", signal.ErrClosed
	}
	dir, id, err := m.transfer.Workspace(base)
	return dir, UsageID(id), err
}

// ImportPlatform 将平台来源交给内部交付组件物化，解析函数不参与持久化。
func (m *MediaCenter) ImportPlatform(ctx context.Context, platformName string, resolver func(context.Context, PlatformMedia, int64) (operationresult.Result[delivery.Source], error), segment PlatformMedia) (operationresult.Result[MediaInfo], error) {
	var resolve func(context.Context, transfer.PlatformMedia, int64) (operationresult.Result[delivery.Source], error)
	if resolver != nil {
		resolve = func(ctx context.Context, segment transfer.PlatformMedia, limit int64) (operationresult.Result[delivery.Source], error) {
			return resolver(ctx, PlatformMedia(segment), limit)
		}
	}
	return mediaInfoResult(m.transfer.ImportPlatform(ctx, platformName, resolve, transfer.PlatformMedia(segment)))
}

func (row HistoryMessage) historyKey() share.HistoryKey {
	return share.HistoryKey{ID: row.ID, Platform: row.Platform, ChatID: row.ChatID, MessageID: row.MessageID, ChatType: row.ChatType}
}

// HistoryIDs 只查询有效历史关联和输出缓存，不下载媒体。
func (m *MediaCenter) HistoryIDs(ctx context.Context, row HistoryMessage) (map[int]string, error) {
	return m.objects.HistoryIDs(ctx, row.historyKey())
}

// AssociateHistory 关联已归档的媒体与历史位置。
func (m *MediaCenter) AssociateHistory(ctx context.Context, row HistoryMessage, index int, kind, id string) error {
	return m.objects.AssociateHistory(ctx, row.historyKey(), index, kind, id)
}

// GetHistoryMediaBatch 保留输入顺序、重复位置、逐项错误及未缓存获取额度。
// 根部转换公开与内部类型，不向调用方暴露存储记录。
func (m *MediaCenter) GetHistoryMediaBatch(ctx context.Context, requests []HistoryMediaRequest, resolver func(context.Context, string, PlatformMedia, int64) (operationresult.Result[delivery.Source], error), opts HistoryFetchOptions) ([]HistoryMediaResult, error) {
	batch := make([]transfer.HistoryMediaRequest, len(requests))
	for i, request := range requests {
		row := request.Message
		message := transfer.HistoryMessage{ID: row.ID, Platform: row.Platform, ChatID: row.ChatID, MessageID: row.MessageID, ChatType: row.ChatType}
		message.Media = make([]transfer.PlatformMedia, len(row.Media))
		for j, segment := range row.Media {
			message.Media[j] = transfer.PlatformMedia(segment)
		}
		batch[i] = transfer.HistoryMediaRequest{Message: message, Index: request.Index}
	}
	var resolve func(context.Context, string, transfer.PlatformMedia, int64) (operationresult.Result[delivery.Source], error)
	if resolver != nil {
		resolve = func(ctx context.Context, platform string, segment transfer.PlatformMedia, limit int64) (operationresult.Result[delivery.Source], error) {
			return resolver(ctx, platform, PlatformMedia(segment), limit)
		}
	}
	results, err := m.transfer.GetHistoryMediaBatch(ctx, batch, resolve, transfer.HistoryFetchOptions(opts))
	var converted []HistoryMediaResult
	if results != nil {
		converted = make([]HistoryMediaResult, len(results))
	}
	for i, result := range results {
		converted[i].Err = result.Err
		converted[i].Media = operationresult.Result[MediaInfo]{Status: result.Media.Status, Message: result.Media.Message}
		if result.Err == nil && result.Media.Status == operationresult.Success {
			converted[i].Media.Data = MediaInfo(result.Media.Data)
		}
	}
	return converted, err
}

// Cleanup 将借用的历史查询传给对象管理，协调对账、过期与孤立对象回收。
func (m *MediaCenter) Cleanup(ctx context.Context) error {
	if err := m.transfer.CleanupUsages(ctx, nil); err != nil {
		return err
	}
	return m.objects.Cleanup(ctx, m.chatHistory)
}
