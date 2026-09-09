package media

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"elbot/internal/storage"
)

// Hold keeps an in-flight consumer alive independently of persistent owners.
func (m *Manager) Hold(ctx context.Context, id string) (func() error, error) {
	ref := storage.MediaReference{MediaID: id, OwnerType: "request", OwnerID: storage.NewID(), Purpose: "temporary"}
	if err := m.AddReference(ctx, &ref); err != nil {
		return nil, err
	}
	return func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return m.RemoveReference(cleanupCtx, ref)
	}, nil
}

type referencedReader struct {
	io.ReadCloser
	release func() error
	once    sync.Once
	err     error
}

func (r *referencedReader) Close() error {
	r.once.Do(func() { r.err = errors.Join(r.ReadCloser.Close(), r.release()) })
	return r.err
}
