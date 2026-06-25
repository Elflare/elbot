package builtin

import (
	"context"
	"fmt"
	"log/slog"

	"elbot/internal/elvena"
	"elbot/internal/hook"
	residentmemory "elbot/internal/hook/plugins/resident_memory"
	"elbot/internal/hook/rules"
	"elbot/internal/memory/resident"
	"elbot/internal/security"
	"elbot/internal/tool"
)

// Options contains shared dependencies for hook plugins shipped with ElBot.
type Options struct {
	ConfigDir           string
	Tools               *tool.Registry
	Policy              *security.Policy
	ResidentMemoryStore *resident.Store
	Logger              *slog.Logger
	Audit               func(event string, attrs ...any)
	Notify              func(context.Context, string)
	Elvena              elvena.Dispatcher
}

func RegisterAll(registrar hook.Registrar, opts Options) error {
	if registrar == nil {
		return nil
	}
	residentMemoryModule := residentmemory.NewModule(residentmemory.Options{Store: opts.ResidentMemoryStore})
	registerModule(registrar, opts, "resident_memory", residentMemoryModule)

	rulesModule, err := rules.NewModule(rules.Options{
		ConfigDir: opts.ConfigDir,
		Tools:     opts.Tools,
		Policy:    opts.Policy,
		Logger:    opts.Logger,
		Audit:     opts.Audit,
		Notify:    opts.Notify,
		Elvena:    opts.Elvena,
	})
	if err == nil {
		registerModule(registrar, opts, "rules", rulesModule)
	} else {
		reportPluginError(opts, "rules", err)
	}
	return nil
}

func registerModule(registrar hook.Registrar, opts Options, name string, module hook.Module) {
	if err := module.RegisterHooks(registrar); err != nil {
		reportPluginError(opts, name, err)
	}
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
