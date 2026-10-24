// 待删除：旧 Hook 模块依赖定义，职责将由根包依赖与装配承接。

package module

import (
	"context"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/hook/rules"
	"elbot/internal/media"
	"elbot/internal/tool"
)

// ModuleDeps contains plugin capabilities only. Requests and turn identity belong to Dialogue.
type ModuleDeps struct {
	ConfigPath      string
	Registry        *tool.Registry
	Media           *media.Manager
	ProcessEnv      hook.ProcessEnvironment
	PlatformCallers rules.PlatformCallerResolver
	Send            func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error)
}
