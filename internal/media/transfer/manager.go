// Package transfer 通过对象契约编排入站、模型请求和平台交付。
package transfer

import (
	"sync"
	"time"

	"elbot/internal/media/share"
)

// Options 只包含交付选择和物化限制，不包含后端凭据。
type Options struct {
	Backend              string        // 模型交付模式：base64、s3 或 hybrid。
	MaxDirectBase64Bytes int64         // 直接内联的请求媒体总字节上限。
	MaxImportBytes       int64         // 默认物化上限，也用于平台解析及预先检查。
	DownloadTimeout      time.Duration // 默认下载期限。
}

// Manager 只借用对象操作，不持有仓储或具体后端。
type Manager struct {
	objects  share.Objects     // 物化、持有与历史关联能力。
	options  Options           // 构造时固定的交付配置。
	usagesMu sync.Mutex        // 保护使用记录集合；不覆盖资源清理。
	usages   map[string]*usage // 包含仍在使用与已结束但尚未清理的资源。
}

// NewManager 接收根部裁剪的对象能力与交付配置。
func NewManager(objects share.Objects, opts Options) *Manager {
	return &Manager{objects: objects, options: opts, usages: make(map[string]*usage)}
}
