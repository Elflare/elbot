package media

import (
	"context"
	"fmt"

	"elbot/internal/config"
	"elbot/internal/storage"
)

// NewConfigured shares one local content store and an optional S3-compatible backend.
func NewConfigured(ctx context.Context, store storage.Store, root string, cfg config.FileDeliveryConfig) (*Manager, error) {
	m := NewManager(store, root, &LocalBackend{Root: root})
	defaults := config.Default().FileDelivery
	if cfg.Backend == "" {
		cfg.Backend = defaults.Backend
	}
	if cfg.MaxDirectBase64Bytes <= 0 {
		cfg.MaxDirectBase64Bytes = defaults.MaxDirectBase64Bytes
	}
	if cfg.S3Region == "" {
		cfg.S3Region = defaults.S3Region
	}
	m.FileDelivery = cfg
	switch cfg.Backend {
	case "base64":
	case "s3", "hybrid":
		remote, err := NewS3Backend(ctx, cfg)
		if err != nil {
			return nil, err
		}
		m.Remote = remote
		if cfg.Backend == "s3" {
			m.Backend = remote
		}
	default:
		return nil, fmt.Errorf("unsupported media backend %q", cfg.Backend)
	}
	return m, nil
}
