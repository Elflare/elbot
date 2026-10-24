// 待删除：转发规则构造与注册，职责将并入根包装配。

package builtin

import (
	"context"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/hook/rules"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/tool"
)

// Options contains shared dependencies for hook plugins shipped with ElBot.
type Options struct {
	ConfigDir string
	Tools     *tool.Registry

	Notices *[]string // Optional reload report collector; never a sending dependency.
	// Send preserves the independent delivery outcome alongside business receipts.
	Send            func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error)
	PlatformCallers rules.PlatformCallerResolver
	Runtime         *hookruntime.Manager
	ProcessEnv      hook.ProcessEnvironment
}

func RegisterAll(registrar hook.Registrar, opts Options) ([]hookruntime.Config, error) {
	rulesModule, err := rules.NewModule(rules.Options{ConfigDir: opts.ConfigDir, Tools: opts.Tools, Notices: opts.Notices, Send: opts.Send, PlatformCallers: opts.PlatformCallers, Runtime: opts.Runtime, ProcessEnv: opts.ProcessEnv})
	if err != nil {
		return nil, err
	}
	rulesModule.RegisterHooks(registrar)
	return append([]hookruntime.Config(nil), rulesModule.Runtimes...), nil
}
