// Package mediatest constructs real media services for consumer integration tests.
package mediatest

import (
	"context"
	"path/filepath"
	"testing"

	"elbot/internal/config"
	"elbot/internal/media"
)

func New(t *testing.T, deps media.MediaCenterDeps, root string, configure ...func(*config.Config)) *media.MediaCenter {
	t.Helper()
	cfg := config.Default()
	cfg.Sandbox.Root = filepath.Join(root, "sandbox")
	cfg.FileDelivery.Backend = "base64"
	for _, apply := range configure {
		apply(cfg)
	}
	center, err := media.NewMediaCenter(t.Context(), cfg, deps)
	if center != nil {
		t.Cleanup(func() { _ = center.Close(context.Background()) })
	}
	if err != nil {
		t.Fatal(err)
	}
	return center
}
