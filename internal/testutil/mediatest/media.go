// Package mediatest constructs real media services for consumer integration tests.
package mediatest

import (
	"context"
	"elbot/internal/operationresult"
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = center.Close(context.Background()) })
	return center
}

// Read 用真实持有读取归档，供跨包测试检查内容和元数据。
func Read(ctx context.Context, center *media.MediaCenter, id string) (operationresult.Result[[]byte], media.MediaInfo, error) {
	item, err := center.Hold(ctx, id)
	if err != nil || item.Status != operationresult.Success {
		return operationresult.Result[[]byte]{Status: item.Status, Message: item.Message}, media.MediaInfo{}, err
	}
	held := item.Data
	defer center.PublishUsageFinished(ctx, []media.UsageID{held.UsageID})
	data, err := held.Read(ctx)
	if err != nil {
		return operationresult.Result[[]byte]{}, media.MediaInfo{}, err
	}
	return operationresult.Result[[]byte]{Status: operationresult.Success, Data: data}, held.Info(), nil
}
