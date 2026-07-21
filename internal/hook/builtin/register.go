package builtin

import (
	"context"
	"fmt"
	"log/slog"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/hook/rules"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/tool"
)

// Options contains shared dependencies for hook plugins shipped with ElBot.
type Options struct {
	ConfigDir       string
	Tools           *tool.Registry
	Logger          *slog.Logger
	Audit           func(event string, attrs ...any)
	Notify          func(context.Context, string)
	Send            func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)
	PlatformCallers rules.PlatformCallerResolver
	Runtime         *hookruntime.Manager
	ProcessEnv      hook.ProcessEnvironment
}

func RegisterAll(registrar hook.Registrar, opts Options) ([]hookruntime.Config, error) {
	if registrar == nil {
		return nil, nil
	}
	rulesModule, err := rules.NewModule(rules.Options{
		ConfigDir:       opts.ConfigDir,
		Tools:           opts.Tools,
		Logger:          opts.Logger,
		Audit:           opts.Audit,
		Notify:          opts.Notify,
		Send:            opts.Send,
		PlatformCallers: opts.PlatformCallers,
		Runtime:         opts.Runtime,
		ProcessEnv:      opts.ProcessEnv,
	})
	if err == nil {
		if err := registerModule(registrar, opts, "rules", rulesModule); err != nil {
			return nil, err
		}
	} else {
		reportPluginError(opts, "rules", err)
		return nil, err
	}
	return append([]hookruntime.Config(nil), rulesModule.Runtimes...), nil
}

func registerModule(registrar hook.Registrar, opts Options, name string, module hook.Module) error {
	if err := module.RegisterHooks(registrar); err != nil {
		reportPluginError(opts, name, err)
		return err
	}
	return nil
}

func reportPluginError(opts Options, name string, err error) {
	if err == nil {
		return
	}
	if opts.Logger != nil {
		opts.Logger.Error("hook plugin disabled", "plugin", name, "error", err)
	}
	if opts.Notify != nil {
		opts.Notify(context.Background(), fmt.Sprintf("Hook 插件 %s 已禁用：%v", name, err))
	}
}
