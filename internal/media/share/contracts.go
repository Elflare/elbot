package share

import (
	"context"
	"elbot/internal/operationresult"
	"io"
	"time"
)

// Objects 是交付组件需要的对象能力，不包含仓储或回收控制。
// 用户操作以 Result 表达业务拒绝，读取和释放持有仍保留原始 IO 故障。
type Objects interface {
	Info(ctx context.Context, id string) (operationresult.Result[MediaInfo], error)
	Hold(ctx context.Context, id string) (operationresult.Result[HeldMedia], error)
	ImportBytes(ctx context.Context, data []byte, input Input, limits ImportLimits) (operationresult.Result[MediaInfo], error)
	ImportURL(ctx context.Context, rawURL string, input Input, limits ImportLimits) (operationresult.Result[MediaInfo], error)
	ImportFile(ctx context.Context, path string, input Input, limits ImportLimits) (operationresult.Result[MediaInfo], error)
	LoadHistoryMedia(ctx context.Context, key HistoryKey) (map[int]HistoryEntry, error)
	AssociateHistory(ctx context.Context, key HistoryKey, index int, kind, id string) error
}

// HeldMedia 让交付组件在持有期间选择读取或远端 URL；记录留在 object。
// Open、SignedURL 的存储和传输故障原样返回。
type HeldMedia interface {
	Info() MediaInfo
	Open(context.Context) (io.ReadCloser, error)
	SignedURL(context.Context) (string, error)
	Close(context.Context) error
}

// Backend 操作对象位置；本地后端不提供签名能力。
// 对象层负责交付有效内容 ID、已确认的内容及大小，并选定读取和删除的位置。
// 后端不重新校验内容，也不从缺失的位置推断存储路径。
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
