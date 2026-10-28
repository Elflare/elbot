package object

import (
	"context"
	"elbot/internal/media/share"
	"elbot/internal/storage"
	"io"
)

// ImportBytes 按本次限制归档字节内容，复用统一导入链。
func (m *Manager) ImportBytes(ctx context.Context, data []byte, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
	panic("TODO")
}

// ImportURL 下载内容，清理来源敏感参数，再交给内容归档。
func (m *Manager) ImportURL(ctx context.Context, rawURL string, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
	panic("TODO")
}

// ImportFile 打开普通文件并归档，调用方负责其业务路径范围。
func (m *Manager) ImportFile(ctx context.Context, path string, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
	panic("TODO")
}

// ImportReader 在限制内读取、识别类型、按需压缩、生成内容 ID 并持久化。
// 对象锁协调去重与回收，返回信息不包含存储位置和后端。
func (m *Manager) ImportReader(ctx context.Context, input io.Reader, size int64, spec share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
	panic("TODO")
}

// sanitizeInput 清理导入名称、类型与可持久化来源。
func sanitizeInput(input share.Input) share.Input { panic("TODO") }

// sanitizeMediaMetadata 清理从仓储读出的内部记录展示信息。
func sanitizeMediaMetadata(item *storage.Media) *storage.Media { panic("TODO") }

// sanitizeSourceURL 移除来源地址中的凭据与敏感查询信息。
func sanitizeSourceURL(raw string) string { panic("TODO") }

// sanitizeSourceFileID 清理平台文件标识，不持久化本地路径。
func sanitizeSourceFileID(id string) string { panic("TODO") }
