package share

import (
	"context"
	"io"
	"time"
)

// Objects 是交付组件需要的对象能力，不包含仓储或回收控制。
type Objects interface {
	Info(ctx context.Context, id string) (MediaInfo, error)
	Hold(ctx context.Context, id string) (HeldMedia, error)
	ImportBytes(ctx context.Context, data []byte, input Input, limits ImportLimits) (MediaInfo, error)
	ImportURL(ctx context.Context, rawURL string, input Input, limits ImportLimits) (MediaInfo, error)
	ImportFile(ctx context.Context, path string, input Input, limits ImportLimits) (MediaInfo, error)
	ImportReader(ctx context.Context, input io.Reader, size int64, spec Input, limits ImportLimits) (MediaInfo, error)
	LoadHistoryMedia(ctx context.Context, key HistoryKey) (map[int]HistoryEntry, error)
	AssociateHistory(ctx context.Context, key HistoryKey, index int, kind, id string) error
}

// HeldMedia 让交付组件在持有期间选择读取或远端 URL；记录留在 object。
type HeldMedia interface {
	Info() MediaInfo
	Open(context.Context) (io.ReadCloser, error)
	SignedURL(context.Context) (string, error)
	Close() error
}

// Backend 操作对象位置；本地后端不提供签名能力。
type Backend interface {
	Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (location string, err error)
	Open(ctx context.Context, location string) (io.ReadCloser, error)
	Remove(ctx context.Context, location string) error
}

// RemoteBackend 增加按对象位置签名的能力，不负责副本记录或签名缓存。
type RemoteBackend interface {
	Backend
	PresignGet(ctx context.Context, location string, expiry time.Duration) (string, error)
}
