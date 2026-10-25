// 统一对接媒体中心，仅向工具暴露通用媒体信息。
package media

import (
	"context"

	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/tool/share"
)

// IDPrefix 是媒体中心统一分配的媒体标识前缀。
const IDPrefix = media.IDPrefix

// ValidID 校验媒体标识的格式，不查询媒体是否存在。
func ValidID(id string) bool {
	return media.ValidID(id)
}

// MediaRuntime 由 Tool 装配共享；媒体中心及其存储细节不向工具开放。
type MediaRuntime struct {
	center      *media.Manager // 借用的媒体中心，生命周期由 App 持有。
	sandboxRoot string         // 媒体缓存根目录，不随调用工作目录变化。
}

func NewMediaRuntime(center *media.Manager, sandboxRoot string) *MediaRuntime {
	return &MediaRuntime{center: center, sandboxRoot: sandboxRoot}
}

func (r *MediaRuntime) Metadata(ctx context.Context, id string) (share.MediaInfo, error) {
	record, err := r.center.Metadata(ctx, id)
	if err != nil {
		return share.MediaInfo{}, err
	}
	return mediaInfo(record), nil
}

// ValidateImage 验证图片内容及 MIME，返回通用媒体信息，不返回图片内部属性。
func (r *MediaRuntime) ValidateImage(ctx context.Context, id string) (share.MediaInfo, error) {
	record, err := r.center.ImageMetadata(ctx, id)
	if err != nil {
		return share.MediaInfo{}, err
	}
	return mediaInfo(record), nil
}

func (r *MediaRuntime) ImportURL(ctx context.Context, url string, input share.MediaImportOptions) (share.MediaInfo, error) {
	record, err := r.center.ImportURL(ctx, url, mediaInput(input))
	if err != nil {
		return share.MediaInfo{}, err
	}
	return mediaInfo(record), nil
}

// ImportFile 接收调用方已按其路径范围解析的本地文件。
func (r *MediaRuntime) ImportFile(ctx context.Context, path string, input share.MediaImportOptions) (share.MediaInfo, error) {
	record, err := r.center.ImportFile(ctx, path, mediaInput(input))
	if err != nil {
		return share.MediaInfo{}, err
	}
	return mediaInfo(record), nil
}

func (r *MediaRuntime) HistoryIDs(ctx context.Context, row storage.ChatMessage) (map[int]string, error) {
	return r.center.HistoryIDs(ctx, row)
}

func (r *MediaRuntime) GetHistoryMediaBatch(ctx context.Context, requests []media.HistoryMediaRequest, resolver platform.MediaResolver, opts media.HistoryFetchOptions) ([]share.HistoryMediaResult, error) {
	results, err := r.center.GetHistoryMediaBatch(ctx, requests, resolver, opts)
	if err != nil {
		return nil, err
	}
	output := make([]share.HistoryMediaResult, len(results))
	for i, result := range results {
		output[i].Err = result.Err
		if result.Err == nil {
			output[i].Media = mediaInfo(result.Media)
		}
	}
	return output, nil
}

func mediaInfo(record *storage.Media) share.MediaInfo {
	return share.MediaInfo{ID: record.ID, Name: record.Name, MIMEType: record.MIMEType, Size: record.Size}
}

func mediaInput(input share.MediaImportOptions) media.Input {
	return media.Input{Name: input.Name, MIMEType: input.MIMEType}
}
