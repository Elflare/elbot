// 管理单次调用的媒体引用、状态与临时资源清理。
package media

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"time"

	"elbot/internal/storage"
	"elbot/internal/tool/share"
)

// MediaCall 由单次调用串行使用；Close 后仅允许重试清理。
type MediaCall struct {
	runtime   *MediaRuntime             // 本次调用借用的统一媒体入口。
	owner     string                    // 本次调用的临时引用所有者。
	mediaByID map[string]*retainedMedia // 已持有的记录及本次调用内的导出状态。
	dir       string                    // 本次调用拥有并在 Close 时删除的临时目录。
}

type retainedMedia struct {
	metadata   *storage.Media         // 内部持有的存储记录，只用于媒体文件操作。
	ref        storage.MediaReference // 防止媒体在调用期间被清理的引用。
	exported   bool                   // 是否已导出到调用私有目录。
	cachedPath string                 // 本次调用已准备的共享缓存路径。
}

func (r *MediaRuntime) NewCall() share.MediaCall {
	return &MediaCall{runtime: r, owner: rand.Text(), mediaByID: map[string]*retainedMedia{}}
}

// Retain 持有媒体到 Close，仅将通用信息交给工具。
func (c *MediaCall) Retain(ctx context.Context, id string) (share.MediaInfo, error) {
	if item, ok := c.mediaByID[id]; ok {
		return mediaInfo(item.metadata), ctx.Err()
	}
	metadata, err := c.runtime.center.Metadata(ctx, id)
	if err != nil {
		return share.MediaInfo{}, err
	}
	return c.retain(ctx, metadata)
}

func (c *MediaCall) retain(ctx context.Context, metadata *storage.Media) (share.MediaInfo, error) {
	if item, ok := c.mediaByID[metadata.ID]; ok {
		return mediaInfo(item.metadata), ctx.Err()
	}
	ref := storage.MediaReference{MediaID: metadata.ID, OwnerType: "skill", OwnerID: c.owner, Purpose: "temporary"}
	if err := c.runtime.center.AddReference(ctx, &ref); err != nil {
		return share.MediaInfo{}, err
	}
	c.mediaByID[metadata.ID] = &retainedMedia{metadata: metadata, ref: ref}
	return mediaInfo(metadata), nil
}

func (c *MediaCall) Close() error {
	var errs []error
	if c.dir != "" {
		errs = append(errs, os.RemoveAll(c.dir))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for id, item := range c.mediaByID {
		if err := c.runtime.center.RemoveReference(ctx, item.ref); err != nil {
			errs = append(errs, err)
		} else {
			delete(c.mediaByID, id)
		}
	}
	return errors.Join(errs...)
}
