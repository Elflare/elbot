package media

import (
	"context"
	"fmt"

	"elbot/internal/config"
	"elbot/internal/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
)

// NewConfigured shares one local content store and an optional lazy S3-compatible backend.
func NewConfigured(_ context.Context, store storage.Store, root string, cfg config.FileDeliveryConfig, credentials aws.CredentialsProvider) (*Manager, error) {
	m := NewManager(store, root, &LocalBackend{Root: root})
	defaults := config.Default()
	if cfg.Backend == "" {
		cfg.Backend = defaults.FileDelivery.Backend
	}
	if cfg.MaxDirectBase64Bytes <= 0 {
		cfg.MaxDirectBase64Bytes = defaults.FileDelivery.MaxDirectBase64Bytes
	}
	if cfg.S3Region == "" {
		cfg.S3Region = defaults.FileDelivery.S3Region
	}
	m.FileDelivery = cfg
	switch cfg.Backend {
	case "base64":
	case "s3", "hybrid":
		m.remoteFactory = func(ctx context.Context) (Backend, error) {
			return NewS3Backend(ctx, cfg, credentials)
		}
		if cfg.Backend == "s3" {
			m.Backend = &lazyBackend{manager: m}
		}
	default:
		return nil, fmt.Errorf("unsupported media backend %q", cfg.Backend)
	}
	return m, nil
}
