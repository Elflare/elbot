package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"elbot/internal/delivery"
	"elbot/internal/storage"
)

// Metadata returns media metadata without reading the object body.
func (m *Manager) Metadata(ctx context.Context, id string) (*storage.Media, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid media ID")
	}
	item, err := m.Store.Media().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return sanitizeMediaMetadata(item), nil
}

// ResolveForOutput exports a temporary file for the existing platform senders.
// The caller must keep it alive until sending completes, then call cleanup.
// Unlike LLM resolution, this never produces a presigned URL.
func (m *Manager) ResolveForOutput(ctx context.Context, id string) (delivery.Source, func(), error) {
	release, err := m.Hold(ctx, id)
	if err != nil {
		return delivery.Source{}, nil, err
	}
	dir, err := os.MkdirTemp("", "elbot-output-*")
	if err != nil {
		_ = release()
		return delivery.Source{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir); _ = release() }
	path := filepath.Join(dir, "media")
	metadata, err := m.Export(ctx, id, path)
	if err != nil {
		cleanup()
		return delivery.Source{}, nil, err
	}
	return delivery.Source{Path: path, MIMEType: metadata.MIMEType}, cleanup, nil
}
