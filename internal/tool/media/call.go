// 管理单次调用的媒体引用、状态与临时资源清理。
package media

import (
	"context"

	"elbot/internal/media"
	"elbot/internal/tool/share"
)

// MediaCall 由单次调用串行使用；Close 发布结束事实。
type MediaCall struct {
	runtime   *MediaRuntime             // 本次调用借用的统一媒体入口。
	mediaByID map[string]*retainedMedia // 已持有的记录及本次调用内的导出状态。
	dir       string                    // 由 Media 持有并负责清理的调用私有目录。
	dirUsage  media.UsageID             // 私有目录对应的独立使用。
}

type retainedMedia struct {
	metadata   media.MediaInfo  // 已持有媒体的公开信息快照。
	held       *media.HeldMedia // 防止媒体在调用期间被清理的持有。
	exported   bool             // 是否已导出到调用私有目录。
	cachedPath string           // 本次调用已准备的共享缓存路径。
}

func (r *MediaRuntime) NewCall() share.MediaCall {
	return &MediaCall{runtime: r, mediaByID: map[string]*retainedMedia{}}
}

// Retain 持有媒体到 Close，仅将通用信息交给工具。
func (c *MediaCall) Retain(ctx context.Context, id string) (share.MediaInfo, error) {
	if item, ok := c.mediaByID[id]; ok {
		return mediaInfo(item.metadata), ctx.Err()
	}
	held, err := c.runtime.center.Hold(ctx, id)
	if err != nil {
		return share.MediaInfo{}, err
	}
	metadata := held.Info()
	c.mediaByID[id] = &retainedMedia{metadata: metadata, held: held}
	return mediaInfo(metadata), nil
}

func (c *MediaCall) retain(ctx context.Context, metadata media.MediaInfo) (share.MediaInfo, error) {
	if item, ok := c.mediaByID[metadata.ID]; ok {
		return mediaInfo(item.metadata), ctx.Err()
	}
	return c.Retain(ctx, metadata.ID)
}

func (c *MediaCall) Close() {
	ids := make([]media.UsageID, 0, len(c.mediaByID)+1)
	if c.dirUsage != "" {
		ids = append(ids, c.dirUsage)
	}
	for _, item := range c.mediaByID {
		ids = append(ids, item.held.UsageID)
	}
	if len(ids) != 0 {
		c.runtime.publishUsageFinished(context.Background(), ids)
	}
	c.mediaByID = nil
	c.dir, c.dirUsage = "", ""
}
