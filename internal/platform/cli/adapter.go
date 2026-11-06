// Package cli 是 CLI 平台入口，负责对外能力与本地、远程实现的装配。
package cli

import (
	"context"

	"elbot/internal/completion"
	"elbot/internal/platform/cli/share"
)

// Adapter 是 CLI 平台总管，持有具体运行实现并协调平台级事件。
type Adapter struct {
	name    string
	runtime share.Runtime
}

func (a *Adapter) Name() string { return a.name }

func (a *Adapter) WaitExit(ctx context.Context) error {
	if runtime, ok := a.runtime.(share.LocalRuntime); ok {
		return runtime.WaitExit(ctx)
	}
	return nil
}

func (a *Adapter) SetCompleter(complete func(context.Context, completion.Request) ([]completion.Item, error)) {
	a.runtime.SetCompleter(complete)
}
