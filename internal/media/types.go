package media

import (
	"context"
	"io"
	"sync"
	"time"

	"elbot/internal/config"
	"elbot/internal/storage"
)

const IDPrefix = "media:"

// PlatformMedia 描述平台来源，不依赖平台实现或存储编码。
type PlatformMedia struct {
	Type           string // 媒体种类：image 或 file。
	MediaID        string // 已导入的媒体 ID，未导入时为空。
	URL            string // 平台媒体来源地址。
	PlatformFileID string // 平台原生文件标识。
	MIMEType       string // 已知内容类型。
	Name           string // 展示文件名。
	Size           int64  // 已知字节数。
}

type HistoryMessage struct {
	ID        string          // 历史记录身份，用于校验关联所有者。
	Platform  string          // 消息所属平台。
	ScopeID   string          // 平台聊天范围。
	MessageID string          // 平台消息 ID。
	Media     []PlatformMedia // 仅含媒体位置，切片下标加一为媒体序号。
}

type Source struct {
	Platform string
	URL      string
	FileID   string
}

// ImportLimits overrides import settings for one materialization call.
// Nonpositive values retain the manager defaults; MaxImportBytes can only
// tighten the manager limit.
type ImportLimits struct {
	MaxImportBytes  int64
	DownloadTimeout time.Duration
}

type Input struct {
	Name     string
	MIMEType string
	Source   Source
}

type Backend interface {
	Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (location string, err error)
	Open(ctx context.Context, media *storage.Media) (io.ReadCloser, error)
	Remove(ctx context.Context, media *storage.Media) error
	PresignGet(ctx context.Context, media *storage.Media, expiry time.Duration) (string, error)
}

type lazyBackend struct {
	manager *Manager
}

type Manager struct {
	objects         objectLocks
	cleanupMu       objectLocks
	local           Backend
	Store           storage.Store
	History         storage.ChatHistoryRepository
	Backend         Backend
	Remote          Backend
	remoteFactory   func(context.Context) (Backend, error)
	remoteMu        sync.Mutex
	MaxImportBytes  int64
	DownloadTimeout time.Duration
	Root            string
	FileDelivery    config.FileDeliveryConfig
	Media           config.MediaConfig

	Now func() time.Time
}
