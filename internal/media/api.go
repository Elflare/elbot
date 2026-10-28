package media

import (
	"context"
	"elbot/internal/signal"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media/object"
	"elbot/internal/media/share"
	"elbot/internal/media/transfer"
)

// MediaCenter 持有内部组件，提供公开业务入口；不公开内部仓储或后端。
type MediaCenter struct {
	closed          atomic.Bool                                      // 关闭后停止接纳新的媒体使用，已结束事实仍可登记。
	objects         *object.Manager                                  // 对象、引用、历史关联与回收。
	transfer        *transfer.Manager                                // 入站、模型请求和平台交付。
	History         History                                          // App 在启动业务前注入；对账和清理调用时传给对象管理。
	usageFinished   *signal.Signal[UsageFinishedEvent, signal.Empty] // 本实例的使用结束通知。
	usageConnection *signal.Connection                               // Media 持有订阅，排空后解除。
}

// Info 查询通用信息，不读内容、不校验图片、不增加持有。
func (m *MediaCenter) Info(ctx context.Context, id string) (MediaInfo, error) {
	info, err := m.objects.Info(ctx, id)
	return MediaInfo(info), err
}

// ValidateImage 验证图片可解码且 MIME 属于图片，返回通用信息。
// 自有持有释放失败时保留错误，并由 Cleanup 重试释放。
func (m *MediaCenter) ValidateImage(ctx context.Context, id string) (MediaInfo, error) {
	info, err := m.objects.ValidateImage(ctx, id)
	return MediaInfo(info), err
}

// ImportBytes 归档字节内容并返回通用信息。
func (m *MediaCenter) ImportBytes(ctx context.Context, data []byte, input Input, limits ImportLimits) (MediaInfo, error) {
	info, err := m.objects.ImportBytes(ctx, data, share.Input{Name: input.Name, MIMEType: input.MIMEType, Source: share.Source(input.Source)}, share.ImportLimits(limits))
	return MediaInfo(info), err
}

// ImportURL 按配置下载、归档内容并清理持久化来源中的敏感信息。
func (m *MediaCenter) ImportURL(ctx context.Context, rawURL string, input Input, limits ImportLimits) (MediaInfo, error) {
	info, err := m.objects.ImportURL(ctx, rawURL, share.Input{Name: input.Name, MIMEType: input.MIMEType, Source: share.Source(input.Source)}, share.ImportLimits(limits))
	return MediaInfo(info), err
}

// ImportFile 归档调用方已按其路径边界解析的普通文件。
func (m *MediaCenter) ImportFile(ctx context.Context, path string, input Input, limits ImportLimits) (MediaInfo, error) {
	info, err := m.objects.ImportFile(ctx, path, share.Input{Name: input.Name, MIMEType: input.MIMEType, Source: share.Source(input.Source)}, share.ImportLimits(limits))
	return MediaInfo(info), err
}

// ImportReader 读取受限内容并归档，size 是调用方已知的长度。
func (m *MediaCenter) ImportReader(ctx context.Context, input io.Reader, size int64, spec Input, limits ImportLimits) (MediaInfo, error) {
	info, err := m.objects.ImportReader(ctx, input, size, share.Input{Name: spec.Name, MIMEType: spec.MIMEType, Source: share.Source(spec.Source)}, share.ImportLimits(limits))
	return MediaInfo(info), err
}

// Open 返回实际读取器和本次使用 ID；关闭读取器后发布使用结束。
func (m *MediaCenter) Open(ctx context.Context, id string) (io.ReadCloser, MediaInfo, UsageID, error) {
	if m.closed.Load() {
		return nil, MediaInfo{}, "", signal.ErrClosed
	}
	held, usageID, err := m.transfer.Hold(ctx, id)
	if err != nil {
		return nil, MediaInfo{}, "", err
	}
	reader, err := held.Open(ctx)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		ids := []string{usageID}
		m.transfer.FinishUsages(ids)
		return nil, MediaInfo{}, "", errors.Join(err, m.transfer.CleanupUsages(ctx, ids))
	}
	return reader, MediaInfo(held.Info()), UsageID(usageID), nil
}

// Read 读取完整内容并结束本次读取持有，保留读取和清理错误。
// 释放失败的自有持有交给 Cleanup 重试。
func (m *MediaCenter) Read(ctx context.Context, id string) ([]byte, MediaInfo, error) {
	data, info, err := m.objects.Read(ctx, id)
	return data, MediaInfo(info), err
}

// Export 写入调用方指定的目标文件，并结束本次读取持有。
// 释放失败的自有持有交给 Cleanup 重试，原调用仍返回错误。
func (m *MediaCenter) Export(ctx context.Context, id, destination string) (MediaInfo, error) {
	info, err := m.objects.Export(ctx, id, destination)
	return MediaInfo(info), err
}

// AcquireForLLM 准备请求副本；实际请求结束后发布对应使用 ID。
func (m *MediaCenter) AcquireForLLM(ctx context.Context, messages []llm.LLMMessage) ([]llm.LLMMessage, UsageID, error) {
	if m.closed.Load() {
		return nil, "", signal.ErrClosed
	}
	prepared, id, err := m.transfer.AcquireForLLM(ctx, messages)
	return prepared, UsageID(id), err
}

// ResolveForOutput 准备发送所需临时文件，不生成模型使用的签名 URL。
// 发送及回执处理结束后发布使用结束；失败资源仍归 Media。
func (m *MediaCenter) ResolveForOutput(ctx context.Context, id string) (delivery.Source, UsageID, error) {
	if m.closed.Load() {
		return delivery.Source{}, "", signal.ErrClosed
	}
	source, usage, err := m.transfer.ResolveForOutput(ctx, id)
	return source, UsageID(usage), err
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
func (m *MediaCenter) ImportPlatform(ctx context.Context, platformName string, resolver func(context.Context, PlatformMedia, int64) (delivery.Source, error), segment PlatformMedia) (MediaInfo, error) {
	var resolve func(context.Context, transfer.PlatformMedia, int64) (delivery.Source, error)
	if resolver != nil {
		resolve = func(ctx context.Context, segment transfer.PlatformMedia, limit int64) (delivery.Source, error) {
			return resolver(ctx, PlatformMedia(segment), limit)
		}
	}
	info, err := m.transfer.ImportPlatform(ctx, platformName, resolve, transfer.PlatformMedia(segment))
	return MediaInfo(info), err
}

// HistoryIDs 只查询有效历史关联和输出缓存，不下载媒体。
func (m *MediaCenter) HistoryIDs(ctx context.Context, row HistoryMessage) (map[int]string, error) {
	return m.objects.HistoryIDs(ctx, share.HistoryKey{ID: row.ID, Platform: row.Platform, ScopeID: row.ScopeID, MessageID: row.MessageID})
}

// AssociateHistory 关联已归档的媒体与历史位置。
func (m *MediaCenter) AssociateHistory(ctx context.Context, row HistoryMessage, index int, kind, id string) error {
	return m.objects.AssociateHistory(ctx, share.HistoryKey{ID: row.ID, Platform: row.Platform, ScopeID: row.ScopeID, MessageID: row.MessageID}, index, kind, id)
}

// GetHistoryMediaBatch 保留输入顺序、重复位置、逐项错误及未缓存获取额度。
// 根部转换公开与内部类型，不向调用方暴露存储记录。
func (m *MediaCenter) GetHistoryMediaBatch(ctx context.Context, requests []HistoryMediaRequest, resolver func(context.Context, string, PlatformMedia, int64) (delivery.Source, error), opts HistoryFetchOptions) ([]HistoryMediaResult, error) {
	var batch []transfer.HistoryMediaRequest
	if requests != nil {
		batch = make([]transfer.HistoryMediaRequest, len(requests))
	}
	for i, request := range requests {
		row := request.Message
		message := transfer.HistoryMessage{ID: row.ID, Platform: row.Platform, ScopeID: row.ScopeID, MessageID: row.MessageID}
		if row.Media != nil {
			message.Media = make([]transfer.PlatformMedia, len(row.Media))
		}
		for j, segment := range row.Media {
			message.Media[j] = transfer.PlatformMedia(segment)
		}
		batch[i] = transfer.HistoryMediaRequest{Message: message, Index: request.Index}
	}
	var resolve func(context.Context, string, transfer.PlatformMedia, int64) (delivery.Source, error)
	if resolver != nil {
		resolve = func(ctx context.Context, platform string, segment transfer.PlatformMedia, limit int64) (delivery.Source, error) {
			return resolver(ctx, platform, PlatformMedia(segment), limit)
		}
	}
	results, err := m.transfer.GetHistoryMediaBatch(ctx, batch, resolve, transfer.HistoryFetchOptions(opts))
	var converted []HistoryMediaResult
	if results != nil {
		converted = make([]HistoryMediaResult, len(results))
	}
	for i, result := range results {
		converted[i] = HistoryMediaResult{Media: MediaInfo(result.Media), Err: result.Err}
	}
	return converted, err
}

// ReconcileHistory 将当前 History 传给对象管理，只移除确认失效的关联。
func (m *MediaCenter) ReconcileHistory(ctx context.Context) error {
	return m.objects.ReconcileHistory(ctx, m.History)
}

// Cleanup 将当前 History 传给对象管理，协调对账、过期与孤立对象回收。
func (m *MediaCenter) Cleanup(ctx context.Context) (events.LogResult, error) {
	if err := m.transfer.CleanupUsages(ctx, nil); err != nil {
		return events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}, err
	}
	return m.objects.Cleanup(ctx, m.History)
}
