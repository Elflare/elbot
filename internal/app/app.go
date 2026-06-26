package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"elbot/internal/agent"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/delivery"
	"elbot/internal/elnis"
	"elbot/internal/elvena"
	"elbot/internal/hook"
	hookbuiltin "elbot/internal/hook/builtin"
	"elbot/internal/llm/openai"
	"elbot/internal/logging"
	"elbot/internal/maintenance"
	"elbot/internal/memory/resident"
	"elbot/internal/platform"
	platformbuiltin "elbot/internal/platform/builtin"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool/builtin"
)

type RunMode string

const (
	RunModeAuto    RunMode = "auto"
	RunModeFull    RunMode = "full"
	RunModeCLIOnly RunMode = "cli"
	RunModeService RunMode = "service"
)

type Options struct {
	ConfigPath string
	Version    string
	StartedAt  time.Time
	Mode       RunMode
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

func resolveRunMode(mode RunMode) (RunMode, error) {
	switch mode {
	case "", RunModeAuto:
		if runtime.GOOS != "windows" && serviceMarkerRunning() {
			fmt.Fprintln(os.Stderr, "ElBot service detected, starting local CLI-only mode. Use `elbot run` to force full foreground mode.")
			return RunModeCLIOnly, nil
		}
		fmt.Fprintln(os.Stderr, "No ElBot service detected, starting full foreground mode. Use `elbot cli` to start local CLI only.")
		return RunModeFull, nil
	case RunModeFull, RunModeCLIOnly, RunModeService:
		return mode, nil
	default:
		return "", fmt.Errorf("unknown run mode %q", mode)
	}
}

func platformMode(mode RunMode) platformbuiltin.Mode {
	switch mode {
	case RunModeCLIOnly:
		return platformbuiltin.ModeCLIOnly
	case RunModeService:
		return platformbuiltin.ModeService
	default:
		return platformbuiltin.ModeFull
	}
}

func shouldStartCron(mode RunMode) bool {
	return mode != RunModeCLIOnly
}

func Run(ctx context.Context, opts Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	mode, err := resolveRunMode(opts.Mode)
	if err != nil {
		return err
	}
	var marker *serviceMarker
	if mode == RunModeService {
		marker, err = claimServiceMarker()
		if err != nil {
			return err
		}
		defer marker.Close()
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

	logs, err := logging.NewManager(cfg.Runtime.LogLevel, cfg.Storage.SessionsSQLitePath, cfg.Runtime.LogRetentionDays)
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
		"elnis_config_path", cfg.ElnisConfigPath,
		"work_provider", cfg.ModeModels["work"].Provider,
		"work_model", cfg.ModeModels["work"].Model,
		"chat_provider", cfg.ModeModels["chat"].Provider,
		"chat_model", cfg.ModeModels["chat"].Model,
		"soul_path", cfg.Soul.Path,
		"sessions_sqlite_path", cfg.Storage.SessionsSQLitePath,
		"chat_history_sqlite_path", cfg.Storage.ChatHistorySQLitePath,
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

	store, err := sqlite.New(ctx, cfg.Storage.SessionsSQLitePath)
	if err != nil {
		return err
	}
	profiler.Mark("sqlite.New")
	defer store.Close()

	chatHistoryStore, err := sqlite.NewChatHistory(ctx, cfg.Storage.ChatHistorySQLitePath)
	if err != nil {
		return err
	}
	profiler.Mark("chat history sqlite.New")
	defer chatHistoryStore.Close()
	chatHistory := chatHistoryStore.Repository()

	maint := maintenance.NewServiceWithConfig(logs, store, chatHistory, cfg, logger)
	cronManager := elcron.NewManager(store.CronJobs(), logger)
	if err := maint.RegisterCronHandlers(cronManager); err != nil {
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

	adapter := openai.NewWithOptions(provider.BaseURL, provider.APIKey, provider.ExtraPayload, modelExtraPayloads(provider.ModelConfigs), appLLMRequestOptions(cfg.LLMRequest, provider.Proxy))
	adapter.SetLogger(logger)
	namingAdapter := adapter
	namingModel := ""
	if cfg.NamingModel.Provider != "" && cfg.NamingModel.Model != "" {
		if namingProvider, ok := cfg.Providers[cfg.NamingModel.Provider]; ok {
			namingAdapter = openai.NewWithOptions(namingProvider.BaseURL, namingProvider.APIKey, namingProvider.ExtraPayload, modelExtraPayloads(namingProvider.ModelConfigs), appLLMRequestOptions(cfg.LLMRequest, namingProvider.Proxy))
			namingAdapter.SetLogger(logger)
			namingModel = cfg.NamingModel.Model
		} else {
			logger.Warn("session naming provider not found, fallback to main model", "provider", cfg.NamingModel.Provider)
		}
	}
	profiler.Mark("llm adapters")
	platforms, err := platformbuiltin.New(platformbuiltin.Options{Mode: platformMode(mode)}, cfg, store, chatHistory, logger)
	if err != nil {
		return err
	}
	profiler.Mark("platform init")
	var agt *agent.Agent
	startupHookNotices := []string{}
	notifyHookIssue := func(ctx context.Context, text string) {
		if agt == nil {
			startupHookNotices = append(startupHookNotices, text)
			return
		}
		_, _ = agt.SendNoticeOutput(ctx, delivery.Target{}, delivery.Text(text))
	}
	cronService := elcron.NewService(elcron.Options{
		Manager:          cronManager,
		Store:            store,
		Logger:           logger,
		EnabledPlatforms: enabledCronPlatforms(cfg),
		SandboxRoot:      cfg.Sandbox.Root,
		Audit: func(event string, attrs ...any) {
			logs.Audit().Log(context.Background(), slog.LevelInfo, "audit event", append([]any{"event", event}, attrs...)...)
		},
		SendTarget: func(ctx context.Context, target delivery.Target, out delivery.Output) (delivery.Receipt, error) {
			if agt == nil {
				return delivery.Receipt{}, fmt.Errorf("agent is not ready")
			}
			return agt.SendNoticeOutput(ctx, target, out)
		},
	})
	if err := cronManager.RegisterHandler(elcron.UserHandlerName, cronService.Handler); err != nil {
		return err
	}
	toolRuntime, err := builtin.NewRuntime(builtin.RuntimeOptions{ConfigDir: filepath.Dir(cfg.ConfigPath), CronService: cronService, ChatHistory: chatHistory, SandboxRoot: cfg.Sandbox.Root, FileDelivery: cfg.FileDelivery, ResidentMemoryMaxUnits: resident.Limits{Core: cfg.ResidentMemory.CoreMaxUnits, Normal: cfg.ResidentMemory.NormalMaxUnits}})
	if err != nil {
		return err
	}
	toolRegistry := toolRuntime.Registry
	residentStore := toolRuntime.ResidentMemoryStore
	skillManager := toolRuntime.SkillManager
	profiler.Mark("builtin tools register")
	if err := skillManager.Reload(ctx); err != nil {
		return err
	}
	profiler.Mark("skill reload")
	securityPolicy := security.NewPolicy(cfg.Security.UserMaxToolRisk, cfg.Security.SuperadminConfirmRisk, cfg.Security.Superadmins)
	var elnisService *elnis.Service
	elvenaBus := elvena.NewBus()
	hooks := hook.NewManager()
	hooks.SetLogger(logger)
	hookOpts := hookbuiltin.Options{
		ConfigDir:           config.PluginConfigDir(cfg.ConfigPath),
		Tools:               toolRegistry,
		Policy:              securityPolicy,
		ResidentMemoryStore: residentStore,
		Logger:              logger,
		Audit: func(event string, attrs ...any) {
			logs.Audit().Log(context.Background(), slog.LevelInfo, "audit event", append([]any{"event", event}, attrs...)...)
		},
		Notify: notifyHookIssue,
		Elvena: elvenaBus,
	}
	if err := hookbuiltin.RegisterAll(hooks, hookOpts); err != nil {
		logger.Error("hook registration failed", "error", err)
		notifyHookIssue(context.Background(), fmt.Sprintf("Hook 注册失败：%v", err))
	}
	if err := registerCronPlatformHook(hooks, cronService); err != nil {
		logger.Error("cron platform hook registration failed", "error", err)
		notifyHookIssue(context.Background(), fmt.Sprintf("Cron Hook 注册失败：%v", err))
	}
	profiler.Mark("hook register")
	agt = agent.NewWithRequestConfig(platforms.Primary, adapter, workModel.Provider, cfg.ModeModels, cfg.Providers, cfg.StateConfigPath, store, cfg.Commands.Prefixes, session.Config{NamingConfig: session.NamingConfig{TriggerStep: cfg.Session.Naming.TriggerStep}, DefaultMode: cfg.Session.DefaultMode}, cfg.NamingModel, namingAdapter, namingModel, namingLogger{logger: logger}, cfg.Soul.Path, cfg.LLMRequest)
	agt.SetHookManager(hooks)
	agt.SetHookReloader(func() error {
		hooks.Reset()
		if err := hookbuiltin.RegisterAll(hooks, hookOpts); err != nil {
			return err
		}
		return registerCronPlatformHook(hooks, cronService)
	})
	agt.SetOutputManager(delivery.NewManager(nil, logger))
	agt.SetSessionListPageSize(cfg.View.SessionListPageSize)
	agt.SetCleanupRetentionDays(cfg.Maintenance.SessionCleanup.RetentionDays)
	agt.SetNonSuperadminIdleTTLMinutes(cfg.Session.NonSuperadminIdleTTLMinutes)
	agt.SetSandboxRoot(cfg.Sandbox.Root)
	agt.SetLogManager(logs)
	agt.SetToolRuntime(toolRegistry, skillManager.Scanner)
	agt.SetToolConfig(cfg.Tools)
	agt.SetToolTagConfig(cfg.ToolTagsConfigPath, cfg.ToolTags)
	agt.SetSecurityPolicy(securityPolicy)
	agt.SetContextOptions(cfg.Context, cfg.ModelMetadata, cfg.Providers, cfg.CompactModel)
	cronService.SetRunner(agt)
	for _, notice := range startupHookNotices {
		notifyHookIssue(context.Background(), notice)
	}
	profiler.Mark("agent init")

	// if cfg.Elnis.Enabled && mode == RunModeCLIOnly {
	// 	logger.Info("elnis disabled in cli-only mode")
	// }
	if cfg.Elnis.Enabled && mode != RunModeCLIOnly {
		elnisTokens, err := resolveElnisTokens(cfg)
		if err != nil {
			return err
		}
		elnisService, err = elnis.NewService(elnis.Options{
			Config:           cfg.Elnis,
			SandboxRoot:      cfg.Sandbox.Root,
			Tokens:           elnisTokens,
			Store:            store,
			Logger:           logs.Elnis(),
			EnabledPlatforms: runtimeNames(platforms.Runtimes),
			PlatformCallers:  platformCallerResolver{runtimes: platforms.Runtimes},
			Audit: func(event string, attrs ...any) {
				logs.Audit().Log(context.Background(), slog.LevelInfo, "audit event", append([]any{"event", event}, attrs...)...)
			},
			Send: func(ctx context.Context, target delivery.Target, out delivery.Output) (delivery.Receipt, error) {
				return agt.SendNoticeOutput(ctx, target, out)
			},
			Runner: agt,
			ResolveModel: func(slot string) config.ModelSelection {
				selected := agt.CurrentModelForMode(slot)
				return config.ModelSelection{Provider: selected.Provider, Model: selected.Model}
			},
		})
		if err != nil {
			return err
		}
		elvenaBus.SetDispatcher(elnisService)
		platforms.Runtimes = append(platforms.Runtimes, elnisRuntimeAdapter{runtime: elnis.NewRuntime(cfg.Elnis.HTTP, elnisService)})
		profiler.Mark("elnis init")
	}

	registerCompletionPlatforms(agt, platforms.Runtimes)
	registerCommandCatalogs(agt, platforms.Runtimes)
	registerPlatformHooks(agt, platforms.Runtimes)
	profiler.Mark("platform hooks")
	startupDuration := profiler.Flush()
	logger.Info("elbot startup completed", "startup_duration", startupDuration.String())
	var afterStart func(context.Context)
	if shouldStartCron(mode) {
		afterStart = func(ctx context.Context) {
			cronStartupScheduled = true
			startCronAsync(ctx, cronManager, cronService, cfg, logger, cronStartupDone)
		}
	}
	return runPlatforms(ctx, agt, logger, platforms.Runtimes, afterStart)
}

type platformRuntime = platform.Runtime

type elnisRuntimeAdapter struct {
	runtime *elnis.Runtime
}

func (a elnisRuntimeAdapter) Name() string { return "elnis" }

func (a elnisRuntimeAdapter) Run(ctx context.Context, handler platform.PlatformHandler) error {
	return a.runtime.Run(ctx)
}

func (a elnisRuntimeAdapter) SendChat(ctx context.Context, out delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, fmt.Errorf("elnis cannot send chat output")
}

func (a elnisRuntimeAdapter) SendNotice(ctx context.Context, target delivery.Target, out delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, fmt.Errorf("elnis cannot send notice output")
}

type platformLifecycle interface {
	StopAppOnExit() bool
}

type platformHookAgent interface {
	RegisterPlatformSender(name string, sender delivery.MessageSender)
	NotifyPlatformConnected(ctx context.Context, platformName string)
}

type completionPlatform interface {
	SetCompleter(*completion.Service)
}

type commandCatalogPlatform interface {
	SetCommandCatalog([]command.Info)
}

func registerCompletionPlatforms(agent *agent.Agent, adapters []platformRuntime) {
	if agent == nil {
		return
	}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		if completer, ok := adapter.(completionPlatform); ok {
			completer.SetCompleter(agent.CompletionService())
		}
	}
}

func registerCronPlatformHook(hooks hook.Registrar, service *elcron.Service) error {
	if hooks == nil || service == nil {
		return nil
	}
	return hooks.Register(hook.Registration{
		Point:  hook.PointPlatformConnected,
		Name:   "builtin.cron.missed_once",
		Match:  hook.Always(),
		Detail: "平台连接时补投递 missed once cron",
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
			service.NotifyPlatformConnected(ctx, event.Platform.Name)
			return event, nil
		}),
	})
}

func registerCommandCatalogs(agent *agent.Agent, adapters []platformRuntime) {
	if agent == nil {
		return
	}
	commands := agent.CommandInfos()
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		if catalog, ok := adapter.(commandCatalogPlatform); ok {
			catalog.SetCommandCatalog(commands)
		}
	}
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
	return maintenance.SetupCron(ctx, manager, cfg)
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

func runtimeNames(runtimes []platformRuntime) []string {
	out := []string{}
	for _, runtime := range runtimes {
		if runtime == nil {
			continue
		}
		name := strings.TrimSpace(runtime.Name())
		if name != "" && name != "elnis" {
			out = append(out, name)
		}
	}
	return out
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

func appLLMRequestOptions(cfg config.LLMRequestConfig, proxy string) openai.RequestOptions {
	return openai.RequestOptions{
		FirstChunkTimeout: time.Duration(cfg.FirstChunkTimeoutSeconds) * time.Second,
		StreamIdleTimeout: time.Duration(cfg.StreamIdleTimeoutSeconds) * time.Second,
		ResponseTimeout:   time.Duration(cfg.ResponseTimeoutSeconds) * time.Second,
		MaxRetries:        cfg.MaxRetries,
		RetryInitialDelay: time.Duration(cfg.RetryInitialDelaySeconds) * time.Second,
		Proxy:             proxy,
	}
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
