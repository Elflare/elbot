package backend

import (
	"context"
	"elbot/internal/media/share"
	"io"
	"sync"
	"time"
)

// LazyBackend 在首次操作时构造远端后端，只缓存成功结果。
type LazyBackend struct {
	factory func(context.Context) (share.RemoteBackend, error) // 根部装配的实际构造函数。
	backend share.RemoteBackend                                // 已成功构造的远端后端。
	mu      sync.Mutex                                         // 串行初始化与成功结果发布。
}

// NewLazyBackend 保存远端构造函数，不在此处访问远端。
func NewLazyBackend(factory func(context.Context) (share.RemoteBackend, error)) *LazyBackend {
	panic("TODO")
}

// Put 初始化后转交对象写入，失败构造允许下次重试。
func (b *LazyBackend) Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (location string, err error) {
	panic("TODO")
}

// Open 初始化后按位置读取对象。
func (b *LazyBackend) Open(ctx context.Context, location string) (io.ReadCloser, error) {
	panic("TODO")
}

// Remove 初始化后删除给定位置。
func (b *LazyBackend) Remove(ctx context.Context, location string) error { panic("TODO") }

// PresignGet 初始化后按位置生成签名，不保存签名缓存。
func (b *LazyBackend) PresignGet(ctx context.Context, location string, expiry time.Duration) (string, error) {
	panic("TODO")
}

var _ share.RemoteBackend = (*LazyBackend)(nil)
