package media

import (
	"context"
	"io"
	"time"

	"elbot/internal/storage"
)

func (b *lazyBackend) backend(ctx context.Context) (Backend, error) {
	return b.manager.remoteBackend(ctx)
}

func (b *lazyBackend) Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (string, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return "", err
	}
	return backend.Put(ctx, id, input, size, contentType)
}

func (b *lazyBackend) Open(ctx context.Context, item *storage.Media) (io.ReadCloser, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return nil, err
	}
	return backend.Open(ctx, item)
}

func (b *lazyBackend) Remove(ctx context.Context, item *storage.Media) error {
	backend, err := b.backend(ctx)
	if err != nil {
		return err
	}
	return backend.Remove(ctx, item)
}

func (b *lazyBackend) PresignGet(ctx context.Context, item *storage.Media, expiry time.Duration) (string, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return "", err
	}
	return backend.PresignGet(ctx, item, expiry)
}
