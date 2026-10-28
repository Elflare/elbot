// Package mediatest constructs real media services for consumer integration tests.
package mediatest

import (
	"context"
	"path/filepath"
	"testing"

	"elbot/internal/config"
	"elbot/internal/media"
)

func New(t *testing.T, store media.Store, root string, configure ...func(*config.Config)) *media.MediaCenter {
	t.Helper()
	cfg := config.Default()
	cfg.Sandbox.Root = filepath.Join(root, "sandbox")
	cfg.FileDelivery.Backend = "base64"
	for _, apply := range configure {
		apply(cfg)
	}
	center, err := media.NewMediaCenter(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = center.Close(context.Background()) })
	return center
}

// Publisher 接入真实结束信号，供消费方测试按生产装配方式注入。
func Publisher(center interface {
	PublishUsageFinished(context.Context, media.UsageFinishedEvent)
}) func(context.Context, []media.UsageID) {
	return func(ctx context.Context, ids []media.UsageID) {
		center.PublishUsageFinished(ctx, media.UsageFinishedEvent{UsageIDs: ids})
	}
}
