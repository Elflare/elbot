package object

import (
	"context"
	"elbot/internal/media/share"
	"elbot/internal/storage"
	"io"
)

// Info 取得通用信息快照，不读取内容或增加引用。
func (m *Manager) Info(ctx context.Context, id string) (share.MediaInfo, error) { panic("TODO") }

// Open 取得单次读取持有，读取器 Close 同时负责释放且允许重试失败释放。
func (m *Manager) Open(ctx context.Context, id string) (io.ReadCloser, share.MediaInfo, error) {
	panic("TODO")
}

// Read 读取完整内容并汇总读取、读取器关闭和引用释放错误。
func (m *Manager) Read(ctx context.Context, id string) ([]byte, share.MediaInfo, error) {
	panic("TODO")
}

// Export 将对象写入目标文件并释放本次持有。
func (m *Manager) Export(ctx context.Context, id, destination string) (share.MediaInfo, error) {
	panic("TODO")
}

// backendForStoredMedia 按内部记录选择后端，不向调用者暴露记录。
func (m *Manager) backendForStoredMedia(ctx context.Context, item *storage.Media) (share.Backend, error) {
	panic("TODO")
}
