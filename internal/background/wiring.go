package background

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	elcron "elbot/internal/background/cron"
	"elbot/internal/background/elnis"
	"elbot/internal/background/execution"
	"elbot/internal/background/maintenance"
	"elbot/internal/config"
)

// NewSupervisor 接收完整配置，装配后台业务组件。
// TODO: 在消费方能力契约确认后，将旧 Agent、Platform 组件依赖替换为总管公开能力。
func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Supervisor{ctx: ctx, cancel: cancel, cfg: cfg, enableWorkers: deps.EnableWorkers, done: make(chan struct{})}
	m.cronManager = elcron.NewManager(deps.Store.CronJobs())
	maint := maintenance.NewServiceWithConfig(deps.Logs, deps.ChatHistory, cfg, deps.Sessions)
	maint.Media = deps.Media
	if err := maint.RegisterCronHandlers(m.cronManager); err != nil {
		return m, err
	}
	runner := execution.NewBackgroundRunner(execution.BackgroundRunnerDeps{
		Sessions:    deps.Sessions,
		SessionRows: deps.Store.Sessions(),
		Identity:    deps.Identity,
		Execution:   deps.Execution,
		Preloader:   deps.Preloader,
		ToolState:   deps.ToolState,
		SandboxRoot: cfg.Sandbox.Root,
	})
	m.runner = runner
	m.cron = elcron.NewService(elcron.Options{Manager: m.cronManager, Store: deps.Store, EnabledPlatforms: enabledCronPlatforms(cfg), SandboxRoot: cfg.Sandbox.Root, SendTarget: deps.Send, Models: deps.Models, Sessions: deps.Sessions})
	if err := m.cronManager.RegisterHandler(elcron.UserHandlerName, m.cron.Handler); err != nil {
		return m, err
	}
	m.cron.SetRunner(runner)
	// Recovery is required even with Elnis or all workers disabled.
	if err := elnis.RecoverInterrupted(ctx, deps.Store.ElnisEvents()); err != nil {
		return m, err
	}
	if cfg.Elnis.Enabled && deps.EnableWorkers {
		tokens, err := resolveElnisTokens(cfg)
		if err != nil {
			return m, err
		}
		service, err := elnis.NewService(elnis.Options{Media: deps.Media, Config: cfg.Elnis, SandboxRoot: cfg.Sandbox.Root, Tokens: tokens, Store: deps.Store, EnabledPlatforms: deps.EnabledPlatforms, PlatformCallers: deps.PlatformCallers, Send: deps.Send, Runner: runner, ToolPreloader: deps.Preloader, ResolveModel: func(slot string) config.ModelSelection { return deps.Models.ResolveMode(slot).ModelSelection }})
		if err != nil {
			return m, err
		}
		m.elnis = elnis.NewRuntime(cfg.Elnis.HTTP, service)
	}
	return m, nil
}

func enabledCronPlatforms(cfg *config.Config) []elcron.PlatformTarget {
	out := []elcron.PlatformTarget{}
	for name, raw := range cfg.Platform {
		if !platformConfigEnabled(raw) {
			continue
		}
		out = append(out, elcron.PlatformTarget{Name: name, SuperadminIDs: cfg.Security.Superadmins[name]})
	}
	return out
}

func platformConfigEnabled(raw map[string]any) bool {
	value, ok := raw["enabled"]
	if !ok {
		return false
	}
	enabled, ok := value.(bool)
	return ok && enabled
}

func resolveElnisTokens(cfg *config.Config) (map[string]string, error) {
	out := map[string]string{}
	configDir := filepath.Dir(cfg.ConfigPath)
	for name, tokenCfg := range cfg.Elnis.Tokens {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for _, envName := range tokenCfg.TokenEnv {
			envName = strings.TrimSpace(envName)
			if envName == "" {
				continue
			}
			value, ok, err := config.ConfigEnv(envName, configDir)
			if err != nil {
				return nil, fmt.Errorf("resolve elnis token %q from %s: %w", name, envName, err)
			}
			if ok && strings.TrimSpace(value) != "" {
				out[name] = strings.TrimSpace(value)
				break
			}
		}
	}
	return out, nil
}
