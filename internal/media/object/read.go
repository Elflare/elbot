package object

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"elbot/internal/media/share"
	"elbot/internal/storage"
)

// Info 取得通用信息快照，不读取内容或增加引用。
func (m *Manager) Info(ctx context.Context, id string) (share.MediaInfo, error) {
	if !share.ValidID(id) {
		return share.MediaInfo{}, fmt.Errorf("invalid media ID")
	}
	item, err := m.media.Get(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return share.MediaInfo{}, &share.UnavailableError{Err: err}
		}
		return share.MediaInfo{}, err
	}
	if item.Deleting {
		return share.MediaInfo{}, &share.UnavailableError{Err: storage.ErrNotFound}
	}
	return mediaInfo(item), nil
}

// mediaInfo 只投影通用信息，不兼容修正已有记录。
func mediaInfo(item *storage.Media) share.MediaInfo {
	return share.MediaInfo{ID: item.ID, Name: item.Name, MIMEType: item.MIMEType, Size: item.Size}
}

// Read 读取完整内容并汇总读取、读取器关闭和引用释放错误。
// 释放失败的自有持有交给 Cleanup 重试。
func (m *Manager) Read(ctx context.Context, id string) (data []byte, info share.MediaInfo, err error) {
	held, err := m.Hold(ctx, id)
	if err != nil {
		return nil, info, err
	}
	defer func() { err = errors.Join(err, m.releaseOwned(held)) }()
	info = held.Info()
	reader, err := held.Open(ctx)
	if err != nil {
		return nil, info, err
	}
	defer func() { err = errors.Join(err, reader.Close()) }()
	data, err = io.ReadAll(reader)
	return data, info, errors.Join(err, ctx.Err())
}

// Export 将对象写入目标文件并释放本次持有。
// 释放失败的自有持有交给 Cleanup 重试。
func (m *Manager) Export(ctx context.Context, id, destination string) (info share.MediaInfo, err error) {
	held, err := m.Hold(ctx, id)
	if err != nil {
		return info, err
	}
	defer func() { err = errors.Join(err, m.releaseOwned(held)) }()
	info = held.Info()
	reader, err := held.Open(ctx)
	if err != nil {
		return info, err
	}
	defer func() { err = errors.Join(err, reader.Close()) }()
	file, err := os.Create(destination)
	if err != nil {
		return info, fmt.Errorf("create media export: %w", err)
	}
	_, err = io.Copy(file, reader)
	return info, errors.Join(err, file.Close(), ctx.Err())
}

// backendForStoredMedia 按内部记录选择后端，不向调用者暴露记录。
func (m *Manager) backendForStoredMedia(item *storage.Media) (share.Backend, error) {
	switch item.Backend {
	case "local":
		return m.local, nil
	case "s3":
		if m.remote == nil {
			return nil, fmt.Errorf("remote media backend is unavailable")
		}
		return m.remote, nil
	default:
		return nil, fmt.Errorf("unsupported stored media backend %q", item.Backend)
	}
}
