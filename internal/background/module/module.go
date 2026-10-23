package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	elcron "elbot/internal/cron"
	"elbot/internal/elnis"
	globalevents "elbot/internal/events"
	"elbot/internal/maintenance"
	"elbot/internal/tool/builtin"
)

// Module owns background producers; borrowed Dialogue and Tool services outlive it.
type Module struct {
	ctx             context.Context
	cancel          context.CancelFunc
	cfg             *config.Config
	cronManager     *elcron.Manager
	cron            *elcron.Service
	runner          *BackgroundRunner
	elnis           *elnis.Runtime
	enableWorkers   bool
	releaseTools    func()
	mu              sync.Mutex
	started, closed bool
	startupDone     chan struct{}
	elnisDone       chan struct{}
	done            chan struct{}
}

func NewModule(ctx context.Context, deps ModuleDeps) (*Module, error) {
	ctx, cancel := context.WithCancel(ctx)
	cfg := &config.Config{ConfigPath: deps.ConfigPath, Maintenance: deps.Maintenance, Elnis: deps.Elnis, Sandbox: config.SandboxConfig{Root: deps.SandboxRoot}, Platform: deps.Platform, Security: config.SecurityConfig{Superadmins: deps.Superadmins}}
	m := &Module{ctx: ctx, cancel: cancel, cfg: cfg, enableWorkers: deps.EnableWorkers, done: make(chan struct{})}
	m.cronManager = elcron.NewManager(deps.Store.CronJobs())
	maint := maintenance.NewServiceWithConfig(deps.Logs, deps.ChatHistory, cfg, deps.Sessions)
	maint.Media = deps.Media
	if err := maint.RegisterCronHandlers(m.cronManager); err != nil {
		return m, err
	}
	runner := NewBackgroundRunner(BackgroundRunnerDeps{
		Sessions:    deps.Sessions,
		SessionRows: deps.Store.Sessions(),
		Identity:    deps.Identity,
		Execution:   deps.Execution,
		Preloader:   deps.Preloader,
		ToolState:   deps.ToolState,
		SandboxRoot: deps.SandboxRoot,
	})
	m.runner = runner
	m.cron = elcron.NewService(elcron.Options{Manager: m.cronManager, Store: deps.Store, EnabledPlatforms: enabledCronPlatforms(cfg), SandboxRoot: deps.SandboxRoot, SendTarget: deps.Send, Models: deps.Models, Sessions: deps.Sessions})
	if err := m.cronManager.RegisterHandler(elcron.UserHandlerName, m.cron.Handler); err != nil {
		return m, err
	}
	m.cron.SetRunner(runner)
	// Recovery is required even with Elnis or all workers disabled.
	if err := elnis.RecoverInterrupted(ctx, deps.Store.ElnisEvents()); err != nil {
		return m, err
	}
	if deps.Elnis.Enabled && deps.EnableWorkers {
		tokens, err := resolveElnisTokens(cfg)
		if err != nil {
			return m, err
		}
		service, err := elnis.NewService(elnis.Options{Media: deps.Media, Config: deps.Elnis, SandboxRoot: deps.SandboxRoot, Tokens: tokens, Store: deps.Store, EnabledPlatforms: deps.EnabledPlatforms, PlatformCallers: deps.PlatformCallers, Send: deps.Send, Runner: runner, ToolPreloader: deps.Preloader, ResolveModel: func(slot string) config.ModelSelection { return deps.Models.ResolveMode(slot).ModelSelection }})
		if err != nil {
			return m, err
		}
		m.elnis = elnis.NewRuntime(deps.Elnis.HTTP, service)
	}
	release, err := deps.Registry.RegisterOwned(builtin.NewCronTools(m.cron, deps.ToolInfo)...)
	if err != nil {
		return m, err
	}
	m.releaseTools = release
	return m, nil
}

func (m *Module) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return context.Canceled
	}
	if m.started {
		return nil
	}
	if err := m.cron.StartPlatformEvents(m.ctx); err != nil {
		return err
	}
	m.started = true
	if m.enableWorkers {
		m.startupDone = make(chan struct{})
		startCronAsync(m.ctx, m.cronManager, m.cfg, m.startupDone)
	}
	if m.elnis != nil {
		m.elnisDone = make(chan struct{})
		go func() {
			defer close(m.elnisDone)
			if err := m.elnis.Run(m.ctx); err != nil && err != context.Canceled {
				_, notifyErr := globalevents.NotificationRequested.EmitAsync(context.WithoutCancel(m.ctx), globalevents.NewNotification(m.ctx, slog.LevelError, "Elnis 服务异常退出："+err.Error()))
				err = errors.Join(err, notifyErr)
				_ = globalevents.EmitLog(m.ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelError, EventName: "platform_stopped_with_error", Module: "app", Type: globalevents.ContentText, Content: fmt.Sprintf("platform stopped with error\nplatform=elnis\nerror=%v", err), ContentLimit: 4096, ResultStatus: globalevents.ResultFailed})
			}
		}()
	}
	return nil
}

func startCronAsync(ctx context.Context, manager *elcron.Manager, cfg *config.Config, done chan<- struct{}) {
	go func() {
		defer close(done)
		startedAt := time.Now()
		if err := setupCron(ctx, manager, cfg); err != nil {
			if !errors.Is(err, context.Canceled) {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelError,
						EventName:    "cron_async_startup_failed",
						Module:       "app",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("cron async startup failed\nduration=%v\nerror=%v", time.Since(startedAt).String(), err.Error()),
						ContentLimit: 4096,
						ResultStatus: globalevents.ResultFailed,
					}
					facts, _ := contextinfo.ExecutionFromContext(ctx)
					if facts.SessionID != "" {
						record.Identity.SessionID = &facts.SessionID
					}
					if facts.RunID != "" {
						record.Identity.RunID = &facts.RunID
					}
					if facts.Attempt != "" {
						record.Identity.Attempt = &facts.Attempt
					}
					if facts.RequestID != "" {
						record.Identity.RequestID = &facts.RequestID
					}
					if facts.RootRequestID != "" {
						record.Identity.RootRequestID = &facts.RootRequestID
					}
					_, notifyErr := globalevents.NotificationRequested.EmitAsync(context.WithoutCancel(ctx), globalevents.NewNotification(ctx, slog.LevelError, "Cron 启动失败："+err.Error()))
					if notifyErr != nil {
						record.Content += fmt.Sprintf("\nnotification_error=%v", notifyErr)
					}
					_ = globalevents.EmitLog(ctx, record)
				}
			}
			return
		}

		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "cron_async_startup_completed",
				Module:       "app",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron async startup completed\nduration=%v", time.Since(startedAt).String()),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			_ = globalevents.EmitLog(ctx, record)
		}

	}()
}

func setupCron(ctx context.Context, manager *elcron.Manager, cfg *config.Config) error {
	return maintenance.SetupCron(ctx, manager, cfg)
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

// Wait keeps an enabled Elnis runtime alive even when service mode has no foreground platform loops.
func (m *Module) Wait(ctx context.Context) error {
	if m.elnisDone == nil {
		return nil
	}
	select {
	case <-m.elnisDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Module) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	return m.runner.RunBackground(ctx, req)
}
