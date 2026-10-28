package backend

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"elbot/internal/media/share"
)

// LazyBackend 在首次操作时构造远端后端，只缓存成功结果。
type LazyBackend struct {
	factory func(context.Context) (share.RemoteBackend, error) // 根部装配的实际构造函数。
	backend share.RemoteBackend                                // 已成功构造的远端后端。
	mu      sync.Mutex                                         // 串行初始化与成功结果发布。
}

// NewLazyBackend 保存远端构造函数，不在此处访问远端。
// 根部保证 factory 有效，且构造成功时返回可用后端。
func NewLazyBackend(factory func(context.Context) (share.RemoteBackend, error)) *LazyBackend {
	return &LazyBackend{factory: factory}
}

// getBackend 串行构造与发布；操作转交后不再持有初始化锁。
func (b *LazyBackend) getBackend(ctx context.Context) (share.RemoteBackend, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.backend != nil {
		return b.backend, nil
	}
	backend, err := b.factory(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize remote media backend: %w", err)
	}
	b.backend = backend
	return backend, nil
}

// Put 初始化后转交对象写入，失败构造允许下次重试。
func (b *LazyBackend) Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (location string, err error) {
	backend, err := b.getBackend(ctx)
	if err != nil {
		return "", err
	}
	return backend.Put(ctx, id, input, size, contentType)
}

// Open 初始化后按位置读取对象。
func (b *LazyBackend) Open(ctx context.Context, location string) (io.ReadCloser, error) {
	backend, err := b.getBackend(ctx)
	if err != nil {
		return nil, err
	}
	return backend.Open(ctx, location)
}

// Remove 初始化后删除给定位置。
func (b *LazyBackend) Remove(ctx context.Context, location string) error {
	backend, err := b.getBackend(ctx)
	if err != nil {
		return err
	}
	return backend.Remove(ctx, location)
}

// PresignGet 初始化后按位置生成签名，不保存签名缓存。
func (b *LazyBackend) PresignGet(ctx context.Context, location string, expiry time.Duration) (string, error) {
	backend, err := b.getBackend(ctx)
	if err != nil {
		return "", err
	}
	return backend.PresignGet(ctx, location, expiry)
}

var _ share.RemoteBackend = (*LazyBackend)(nil)
