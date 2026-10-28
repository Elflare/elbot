package media

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"elbot/internal/storage"
)

type LocalBackend struct {
	Root string
}

func (b *LocalBackend) Put(_ context.Context, id string, input io.Reader, _ int64, _ string) (string, error) {
	path := b.path(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create media directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".media-*")
	if err != nil {
		return "", fmt.Errorf("create media temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, input); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write media: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close media temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if _, statErr := os.Stat(path); statErr != nil {
			return "", fmt.Errorf("store media: %w", err)
		}
	}
	return path, nil
}

func (b *LocalBackend) Open(_ context.Context, m *storage.Media) (io.ReadCloser, error) {
	path := m.LocalPath
	if path == "" {
		path = b.path(m.ID)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open media: %w", err)
	}
	return file, nil
}

func (b *LocalBackend) PresignGet(_ context.Context, _ *storage.Media, _ time.Duration) (string, error) {
	return "", fmt.Errorf("local media backend does not support presigned URLs")
}

func (b *LocalBackend) Remove(_ context.Context, m *storage.Media) error {
	path := m.LocalPath
	if path == "" {
		path = b.path(m.ID)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove media: %w", err)
	}
	return nil
}

func (b *LocalBackend) path(id string) string {
	hash := id[len(IDPrefix):]
	return filepath.Join(b.Root, hash[:2], hash[2:])
}
