package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"elbot/internal/agent"
	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/hook"
	hookbuiltin "elbot/internal/hook/builtin"
	"elbot/internal/llm/openai"
	"elbot/internal/logging"
	"elbot/internal/maintenance"
	"elbot/internal/memory/resident"
	"elbot/internal/output"
	"elbot/internal/platform"
	platformbuiltin "elbot/internal/platform/builtin"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/skill"
)

type Options struct {
	ConfigPath string
	Version    string
	StartedAt  time.Time
}

type startupProfiler struct {
	enabled   bool
	startedAt time.Time
	last      time.Time
	entries   []startupProfileEntry
}

type startupProfileEntry struct {
	name     string
	duration time.Duration
	total    time.Duration
}

func newStartupProfiler(startedAt time.Time) startupProfiler {
	now := time.Now()
	if startedAt.IsZero() {
		startedAt = now
	}
	return startupProfiler{startedAt: startedAt, last: startedAt}
}

func (p *startupProfiler) SetEnabled(enabled bool) {
	p.enabled = enabled
}

func (p *startupProfiler) Mark(name string) {
	now := time.Now()
	duration := now.Sub(p.last)
	total := now.Sub(p.startedAt)
	p.last = now
	if p.enabled {
		p.entries = append(p.entries, startupProfileEntry{name: name, duration: duration, total: total})
	}
}

func (p *startupProfiler) Flush() time.Duration {
	total := time.Since(p.startedAt)
	if p.enabled {
		for _, entry := range p.entries {
			fmt.Fprintf(os.Stderr, "[startup] %-24s took=%s total=%s\n", entry.name, entry.duration, entry.total)
		}
	}
	fmt.Fprintf(os.Stderr, "elbot startup completed in %s\n", total)
	return total
}

func startupProfileEnabled(level string) bool {
	return strings.EqualFold(strings.TrimSpace(level), "debug")
}

func Run(ctx context.Context, opts Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	configPath := opts.ConfigPath

	profiler := newStartupProfiler(opts.StartedAt)
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	profiler.SetEnabled(startupProfileEnabled(cfg.Runtime.LogLevel))
	profiler.Mark("config.Load")
	ctx = builtin.WithConfigEnvDir(ctx, filepath.Dir(cfg.ConfigPath))

	logs, err := logging.NewManager(cfg.Runtime.LogLevel, cfg.Storage.SQLitePath, cfg.Runtime.LogRetentionDays)
	if err != nil {
		return err
	}
	profiler.Mark("logging.NewManager")
	defer logs.Close()
	logger := logs.Runtime()
	logger.Info("elbot started",
		"version", opts.Version,
		"config_path", cfg.ConfigPath,
		"providers_config_path", cfg.ProvidersConfigPath,
		"state_config_path", cfg.StateConfigPath,
		"work_provider", cfg.ModeModels["work"].Provider,
		"work_model", cfg.ModeModels["work"].Model,
		"chat_provider", cfg.ModeModels["chat"].Provider,
		"chat_model", cfg.ModeModels["chat"].Model,
		"soul_path", cfg.Soul.Path,
		"sqlite_path", cfg.Storage.SQLitePath,
	)

	workModel := cfg.ModeModels["work"]
	if workModel.Provider == "" || workModel.Model == "" {
		fmt.Fprintf(os.Stderr, "elbot: no work model configured. Set [mode_models.work] provider/model in %s or %s\n", cfg.ProvidersConfigPath, cfg.StateConfigPath)
		return fmt.Errorf("no work model configured")
	}

	provider, ok := cfg.Providers[workModel.Provider]
	if !ok {
		return fmt.Errorf("provider %q not found in config", workModel.Provider)
	}

	store, err := sqlite.New(ctx, cfg.Storage.SQLitePath)
	if err != nil {
		return err
	}
	profiler.Mark("sqlite.New")
	defer store.Close()

	maint := maintenance.NewService(logs, store, cfg.Session.Cleanup, logger)
	cronManager := elcron.NewManager(store.CronJobs(), logger)
	if err := cronManager.RegisterHandler("maintenance.log_cleanup", func(ctx context.Context, job storage.CronJob) error {
		return maint.RunLogCleanup(ctx)
	}); err != nil {
		return err
	}
	if err := cronManager.RegisterHandler("maintenance.session_cleanup", func(ctx context.Context, job storage.CronJob) error {
		return maint.RunSessionCleanup(ctx)
	}); err != nil {
		return err
	}
	profiler.Mark("cron async prepared")
	cronStartupDone := make(chan struct{})
	cronStartupScheduled := false
	defer func() {
		if cronStartupScheduled {
			<-cronStartupDone
		}
		<-cronManager.Stop().Done()
	}()

	adapter := openai.NewWithModelExtraPayloads(provider.BaseURL, provider.APIKey, provider.ExtraPayload, modelExtraPayloads(provider.ModelConfigs))
	adapter.SetLogger(logger)
	namingAdapter := adapter
	namingModel := ""
	if cfg.NamingModel.Provider != "" && cfg.NamingModel.Model != "" {
		if namingProvider, ok := cfg.Providers[cfg.NamingModel.Provider]; ok {
			namingAdapter = openai.NewWithModelExtraPayloads(namingProvider.BaseURL, namingProvider.APIKey, namingProvider.ExtraPayload, modelExtraPayloads(namingProvider.ModelConfigs))
			namingAdapter.SetLogger(logger)
			namingModel = cfg.NamingModel.Model
		} else {
			logger.Warn("session naming provider not found, fallback to main model", "provider", cfg.NamingModel.Provider)
		}
	}
	profiler.Mark("llm adapters")
	platforms, err := platformbuiltin.New(cfg, store, logger)
	if err != nil {
		return err
	}
	profiler.Mark("platform init")
	var agt *agent.Agent
	cronService := elcron.NewService(elcron.Options{
		Manager:          cronManager,
		Store:            store,
		Logger:           logger,
		EnabledPlatforms: enabledCronPlatforms(cfg),
		Audit: func(event string, attrs ...any) {
			logs.Audit().Log(context.Background(), slog.LevelInfo, "audit event", append([]any{"event", event}, attrs...)...)
		},
		SendTarget: func(ctx context.Context, target output.Target, out output.Output) error {
			if agt == nil {
				return fmt.Errorf("agent is not ready")
			}
			_, err := agt.SendNoticeOutput(ctx, target, out)
			return err
		},
	})
	if err := cronManager.RegisterHandler(elcron.UserHandlerName, cronService.Handler); err != nil {
		return err
	}
	toolRegistry := tool.NewRegistry()
	residentStore := resident.NewStore(filepath.Join(filepath.Dir(cfg.ConfigPath), "memories.toml"))
	skillManager := skill.NewManager("", toolRegistry)
	if err := builtin.RegisterAll(toolRegistry, builtin.RegisterOptions{ResidentMemoryStore: residentStore, SkillManager: skillManager, CronService: cronService}); err != nil {
		return err
	}
	profiler.Mark("builtin tools register")
	if err := skillManager.Reload(ctx); err != nil {
		return err
	}
	profiler.Mark("skill reload")
	securityPolicy := security.NewPolicy(cfg.Security.UserMaxToolRisk, cfg.Security.SuperadminConfirmRisk, cfg.Security.Superadmins)
	hooks := hook.NewManager()
	hooks.SetLogger(logger)
	if err := hookbuiltin.RegisterAll(hooks, hookbuiltin.Options{
		ConfigDir:           config.PluginConfigDir(cfg.ConfigPath),
		Tools:               toolRegistry,
		Policy:              securityPolicy,
		ResidentMemoryStore: residentStore,
		Logger:              logger,
		Audit: func(event string, attrs ...any) {
			logs.Audit().Log(context.Background(), slog.LevelInfo, "audit event", append([]any{"event", event}, attrs...)...)
		},
	}); err != nil {
		return err
	}
	if err := registerCronPlatformHook(hooks, cronService); err != nil {
		return err
	}
	profiler.Mark("hook register")
	agt = agent.NewWithOptions(platforms.Primary, adapter, workModel.Provider, cfg.ModeModels, cfg.Providers, cfg.StateConfigPath, store, cfg.Commands.Prefixes, session.Config{NamingConfig: session.NamingConfig{TriggerStep: cfg.Session.Naming.TriggerStep}, DefaultMode: cfg.Session.DefaultMode}, cfg.NamingModel, namingAdapter, namingModel, namingLogger{logger: logger}, cfg.Soul.Path)
	agt.SetHookManager(hooks)
	agt.SetOutputManager(output.NewManager(nil, logger))
	agt.SetSessionListPageSize(cfg.View.SessionListPageSize)
	agt.SetCleanupRetentionDays(cfg.Session.Cleanup.RetentionDays)
	agt.SetNonSuperadminIdleTTLMinutes(cfg.Session.NonSuperadminIdleTTLMinutes)
	agt.SetLogManager(logs)
	agt.SetToolRuntime(toolRegistry, skillManager.Scanner)
	agt.SetToolConfig(cfg.Tools)
	agt.SetSecurityPolicy(securityPolicy)
	agt.SetContextOptions(cfg.Context, cfg.ModelMetadata, cfg.CompactModel)
	cronService.SetRunner(agt)
	profiler.Mark("agent init")

	registerPlatformHooks(agt, platforms.Runtimes)
	profiler.Mark("platform hooks")
	startupDuration := profiler.Flush()
	logger.Info("elbot startup completed", "startup_duration", startupDuration.String())
	return runPlatforms(ctx, agt, logger, platforms.Runtimes, func(ctx context.Context) {
		cronStartupScheduled = true
		startCronAsync(ctx, cronManager, cronService, cfg, logger, cronStartupDone)
	})
}

type platformRuntime = platform.Runtime

type platformLifecycle interface {
	StopAppOnExit() bool
}

type platformHookAgent interface {
	RegisterPlatformSender(name string, sender platform.MessageSender)
	NotifyPlatformConnected(ctx context.Context, platformName string)
}

func registerCronPlatformHook(hooks hook.Registrar, service *elcron.Service) error {
	if hooks == nil || service == nil {
		return nil
	}
	return hooks.Register(hook.Registration{
		Point: hook.PointPlatformConnected,
		Name:  "builtin.cron.missed_once",
		Match: hook.Always(),
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
			service.NotifyPlatformConnected(ctx, event.Platform.Name)
			return event, nil
		}),
	})
}

func registerPlatformHooks(agent platformHookAgent, adapters []platformRuntime) {
	if agent == nil {
		return
	}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		agent.RegisterPlatformSender(adapter.Name(), adapter)
		if notifier, ok := adapter.(platform.ConnectNotifier); ok {
			name := adapter.Name()
			notifier.SetConnectNotifier(func(ctx context.Context, platformName string) {
				if platformName == "" {
					platformName = name
				}
				agent.NotifyPlatformConnected(ctx, platformName)
			})
		}
	}
}

func startCronAsync(ctx context.Context, manager *elcron.Manager, service *elcron.Service, cfg *config.Config, logger *slog.Logger, done chan<- struct{}) {
	go func() {
		defer close(done)
		startedAt := time.Now()
		if err := setupCron(ctx, manager, cfg); err != nil {
			if logger != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("cron async startup failed", "duration", time.Since(startedAt).String(), "error", err.Error())
			}
			return
		}
		if logger != nil {
			logger.Info("cron async startup completed", "duration", time.Since(startedAt).String())
		}
	}()
}

func setupCron(ctx context.Context, manager *elcron.Manager, cfg *config.Config) error {
	if manager == nil || cfg == nil {
		return nil
	}
	if cfg.Maintenance.LogCleanup.Enabled {
		if _, err := manager.UpsertJob(ctx, elcron.UpsertJobRequest{Name: "system.maintenance.log_cleanup", Handler: "maintenance.log_cleanup", Schedule: cfg.Maintenance.LogCleanup.Schedule, Enabled: true}); err != nil {
			return err
		}
	} else if err := manager.DisableJob(ctx, "system.maintenance.log_cleanup"); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	if cfg.Session.Cleanup.Enabled {
		if _, err := manager.UpsertJob(ctx, elcron.UpsertJobRequest{Name: "system.maintenance.session_cleanup", Handler: "maintenance.session_cleanup", Schedule: cfg.Maintenance.LogCleanup.Schedule, Enabled: true}); err != nil {
			return err
		}
	} else if err := manager.DisableJob(ctx, "system.maintenance.session_cleanup"); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	return manager.Start(ctx)
}

func platformStopsAppOnExit(adapter platformRuntime) bool {
	lifecycle, ok := adapter.(platformLifecycle)
	return ok && lifecycle.StopAppOnExit()
}

func runPlatforms(ctx context.Context, handler platform.PlatformHandler, logger *slog.Logger, adapters []platformRuntime, afterStart func(context.Context)) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for _, adapter := range adapters {
		adapter := adapter
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := adapter.Run(runCtx, handler); err != nil && !errors.Is(err, context.Canceled) && logger != nil {
				logger.WarnContext(runCtx, "platform stopped with error", "platform", adapter.Name(), "error", err.Error())
			}
			if platformStopsAppOnExit(adapter) {
				cancel()
			}
		}()
	}
	if afterStart != nil {
		afterStart(runCtx)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		cancel()
		<-done
		return ctx.Err()
	case <-done:
		return nil
	}
}

func enabledCronPlatforms(cfg *config.Config) []elcron.PlatformTarget {
	if cfg == nil {
		return nil
	}
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

func modelExtraPayloads(modelConfigs map[string]config.ModelConfig) map[string]map[string]any {
	out := map[string]map[string]any{}
	for model, cfg := range modelConfigs {
		if cfg.ExtraPayload != nil {
			out[model] = cfg.ExtraPayload
		}
	}
	return out
}
