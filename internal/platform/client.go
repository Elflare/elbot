// client.go 提供 RunClient 入口并向 App 交付客户端运行结果。

package platform

import (
	"context"
	"path/filepath"

	"elbot/internal/config"
	"elbot/internal/platform/cli"
)

// RunClient 只转接 CLI 已分类的结果，不重复解析 URL、拨号错误或回退条件。
func RunClient(ctx context.Context, cfg *config.Config, name string) ClientResult {
	result := cli.RunClient(ctx, cfg.Platform["cli"], filepath.Dir(cfg.ConfigPath), name)
	switch result.Kind {
	case cli.ClientCompleted:
		return ClientResult{Kind: ClientCompleted}
	case cli.ClientFallback:
		return ClientResult{Kind: ClientFallback, Err: result.Err}
	default:
		return ClientResult{Kind: ClientFailed, Err: result.Err}
	}
}
