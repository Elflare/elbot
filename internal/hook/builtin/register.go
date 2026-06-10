package builtin

import (
	"context"
	"log/slog"

	"elbot/internal/hook"
	"elbot/internal/hook/plugins/emoticon"
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
}

func RegisterAll(registrar hook.Registrar, opts Options) error {
	if registrar == nil {
		return nil
	}
	rulesModule, err := rules.NewModule(rules.Options{
		ConfigDir: opts.ConfigDir,
		Tools:     opts.Tools,
		Policy:    opts.Policy,
		Logger:    opts.Logger,
		Audit:     opts.Audit,
		Notify:    opts.Notify,
	})

	if err != nil {
		return err
	}
	emoticonModule, err := emoticon.NewModule(emoticon.Options{ConfigDir: opts.ConfigDir, Logger: opts.Logger})
	if err != nil {
		return err
	}
	residentMemoryModule := residentmemory.NewModule(residentmemory.Options{Store: opts.ResidentMemoryStore})
	modules := []hook.Module{
		residentMemoryModule,
		rulesModule,
		emoticonModule,
	}
	for _, module := range modules {
		if err := module.RegisterHooks(registrar); err != nil {
			return err
		}
	}
	return nil
}
