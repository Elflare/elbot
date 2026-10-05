package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"elbot/internal/agent"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	elcron "elbot/internal/cron"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/doctor"
	"elbot/internal/elvena"
	"elbot/internal/fileops"
	"elbot/internal/hook"
	hookbuiltin "elbot/internal/hook/builtin"
	hookcontrol "elbot/internal/hook/control"
	hookrules "elbot/internal/hook/rules"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/notification"
	notificationrules "elbot/internal/notification/rules"
	platformbuiltin "elbot/internal/platform/builtin"
	"elbot/internal/processenv"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/runtimeinfo"
	"elbot/internal/toolrun"
)

type defaultRuntimeFactory struct{}

func (defaultRuntimeFactory) Build(ctx context.Context, req RuntimeRequest) (*RuntimeComponents, error) {
	foundation := req.Foundation
	cfg := foundation.Config
	logger := foundation.Logger
	models, err := modelmgr.New(modelmgr.Options{
		Clients: req.Models.ByProvider, Providers: cfg.Providers, ModeModels: cfg.ModeModels,
		CompactModel: cfg.CompactModel, NamingModel: cfg.NamingModel,
		StatePath: cfg.StateConfigPath, DefaultMode: cfg.Session.DefaultMode,
	})
	if err != nil {
		return nil, err
	}
	dotEnv, err := config.LoadDotEnv(filepath.Dir(cfg.ConfigPath))
	if err != nil {
		return nil, fmt.Errorf("load process environment: %w", err)
	}
	baseProcessEnv := processenv.New(os.Environ())
	shellProcessEnv := baseProcessEnv.Fill(dotEnv)
	hookProcessEnv := hook.ProcessEnvironment(baseProcessEnv)
	fileDeliveryCredentials, err := resolveFileDeliveryCredentials(cfg.FileDelivery, filepath.Dir(cfg.ConfigPath))
	if err != nil {
		logger.Warn("S3 media backend is unavailable; remote operations will fail until configuration is fixed", "error", err)
		fileDeliveryCredentials = nil
	}
	mediaCenter, err := media.NewConfigured(ctx, foundation.Store, filepath.Join(filepath.Dir(cfg.Sandbox.Root), "media"), cfg.FileDelivery, fileDeliveryCredentials)
	if err != nil {
		return nil, err
	}
	if err := foundation.Store.Media().RecoverInterrupted(ctx); err != nil {
		return nil, err
	}
	mediaCenter.History = foundation.ChatHistory
	if err := mediaCenter.ReconcileHistory(ctx); err != nil {
		return nil, err
	}
	mediaCenter.MaxImportBytes = cfg.PlatformFiles.MaxReceiveFileBytes
	mediaCenter.DownloadTimeout = time.Duration(cfg.PlatformFiles.DownloadTimeoutSecs) * time.Second
	mediaCenter.Media = cfg.Media
	mediaCenter.Logger = logger
	if foundation.Maintenance != nil {
		foundation.Maintenance.Media = mediaCenter
	}
	dispatcher := dispatch.New(dispatch.Options{Primary: req.Platforms.Primary, Store: foundation.Store, Media: mediaCenter, MediaRetentionDays: cfg.Maintenance.SandboxCleanup.RetentionDays, Logger: logger})
	for _, adapter := range req.Platforms.Runtimes {
		if adapter != nil {
			dispatcher.RegisterPlatformSender(adapter.Name(), adapter)
		}
	}
	notices := notification.New(dispatcher, logger, req.Platforms.Primary != nil && req.Platforms.Primary.Name() == "service")
	models.SetRetryNotifier(notificationrules.ModelRetry(notices))
	sendNotice := func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
		return dispatcher.SendNotice(ctx, delivery.Notice{Target: target, Outputs: outputs})
	}
	cronService, err := buildCronService(ctx, foundation, sendNotice)
	if err != nil {
		return nil, err
	}
	files := fileops.NewService(nil)
	toolRuntime, err := builtin.NewRuntime(builtin.RuntimeOptions{
		FileRollback: files,
		ConfigDir:    filepath.Dir(cfg.ConfigPath),
		RuntimeInfo: runtimeinfo.Info{
			ConfigPath:   cfg.ConfigPath,
			SandboxRoot:  cfg.Sandbox.Root,
			FileDelivery: cfg.FileDelivery,
		},
		CronService:            cronService,
		ChatHistory:            foundation.ChatHistory,
		Store:                  foundation.Store,
		Media:                  mediaCenter,
		ResidentMemoryMaxUnits: resident.Limits{Core: cfg.ResidentMemory.CoreMaxUnits, Normal: cfg.ResidentMemory.NormalMaxUnits},
		ProcessEnv:             shellProcessEnv,
	})
	if err != nil {
		return nil, err
	}
	req.Profiler.Mark("builtin tools register")
	toolRuntime.SkillManager.StartDelayedReload(ctx, time.Second)
	req.Profiler.Mark("skill reload scheduled")

	hooks := hook.NewManager()
	hooks.SetLogger(logger)
	securityPolicy := security.NewPolicy(cfg.Security.UserMaxToolRisk, cfg.Security.SuperadminConfirmRisk, cfg.Security.Superadmins)
	elvenaBus := elvena.NewBus()

	notifyHookIssue := func(ctx context.Context, text string) {
		notices.Text(ctx, slog.LevelWarn, text)
	}

	hookRuntime := hookruntime.NewManager(hookruntime.Options{
		Media:      mediaCenter,
		Registry:   toolRuntime.Registry,
		Logger:     logger,
		Audit:      auditFunc(foundation.Logs),
		Send:       sendNotice,
		SharedDir:  filepath.Join(config.PluginConfigDir(cfg.ConfigPath), "_shared"),
		ProcessEnv: hookProcessEnv,
	})

	hookService := buildHookService(foundation, req.Platforms, toolRuntime, cronService, hooks, hookRuntime, hookProcessEnv, notifyHookIssue, sendNotice)
	req.Profiler.Mark("hook register")

	agt, err := buildAgent(foundation, models, req.Platforms, toolRuntime, securityPolicy, hooks, hookRuntime, hookService, dispatcher, notices)
	if err != nil {
		if closeErr := hookRuntime.Close(context.Background()); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup hook runtime after agent build: %w", closeErr))
		}
		return nil, err
	}
	cronService.SetRunner(agt)
	req.Profiler.Mark("agent init")

	bindings := &signalBindings{}
	if err := bindings.connectSession(agt.SessionService(), toolRuntime.FileRollback.Manager, foundation.Logger); err != nil {
		return nil, errors.Join(err, bindings.Close(context.Background()), hookRuntime.Close(context.Background()))
	}
	if foundation.Maintenance != nil {
		foundation.Maintenance.Sessions = agt.SessionService()
	}
	return &RuntimeComponents{
		Dispatcher:    dispatcher,
		Notifications: notices,
		Models:        models,
		Signals:       bindings,
		Media:         mediaCenter,
		Agent:         agt,
		Handler:       agt,
		CronService:   cronService,
		ElvenaBus:     elvenaBus,
		Lifecycle:     hookRuntimeLifecycle{runtime: hookRuntime},
	}, nil
}

func resolveFileDeliveryCredentials(cfg config.FileDeliveryConfig, configDir string) (aws.CredentialsProvider, error) {
	backend := strings.TrimSpace(cfg.Backend)
	if backend == "" {
		backend = config.Default().FileDelivery.Backend
	}
	if backend != "s3" && backend != "hybrid" {
		return nil, nil
	}
	resolve := func(name, label string) (string, error) {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", fmt.Errorf("%s environment variable name is empty", label)
		}
		value, ok, err := config.ConfigEnv(name, configDir)
		if err != nil {
			return "", fmt.Errorf("resolve %s environment variable %q: %w", label, name, err)
		}
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			return "", fmt.Errorf("%s environment variable %q is not configured", label, name)
		}
		return value, nil
	}
	accessKey, err := resolve(cfg.S3AccessKeyEnv, "s3 access key")
	if err != nil {
		return nil, err
	}
	secretKey, err := resolve(cfg.S3SecretKeyEnv, "s3 secret key")
	if err != nil {
		return nil, err
	}
	return credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""), nil
}

func buildCronService(ctx context.Context, foundation *FoundationComponents, send func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)) (*elcron.Service, error) {
	cfg := foundation.Config
	service := elcron.NewService(elcron.Options{
		Manager:          foundation.CronManager,
		Store:            foundation.Store,
		Logger:           foundation.Logger,
		EnabledPlatforms: enabledCronPlatforms(cfg),
		SandboxRoot:      cfg.Sandbox.Root,
		Audit:            auditFunc(foundation.Logs),
		SendTarget:       send,
	})
	if err := service.MigrateLegacyDeliveryState(ctx); err != nil {
		return nil, err
	}
	if err := foundation.CronManager.RegisterHandler(elcron.UserHandlerName, service.Handler); err != nil {
		return nil, err
	}
	return service, nil
}

func buildHookService(
	foundation *FoundationComponents,
	platforms PlatformComponents,
	toolRuntime *builtin.Runtime,
	cronService *elcron.Service,
	hooks *hook.DefaultManager,
	hookRuntime *hookruntime.Manager,
	hookProcessEnv hook.ProcessEnvironment,
	notifyHookIssue func(context.Context, string),
	sendNotice func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error),
) *hookcontrol.Service {
	cfg := foundation.Config
	hookOpts := hookbuiltin.Options{
		ConfigDir:       config.PluginConfigDir(cfg.ConfigPath),
		Tools:           toolRuntime.Registry,
		Logger:          foundation.Logger,
		Audit:           auditFunc(foundation.Logs),
		Notify:          notifyHookIssue,
		Send:            sendNotice,
		PlatformCallers: hookPlatformCallerResolver{runtimes: platforms.Runtimes},
		Runtime:         hookRuntime,
		ProcessEnv:      hookProcessEnv,
	}

	loadHooks := func(registrar hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error) {
		var notices []string
		loadOpts := hookOpts
		loadOpts.Notify = func(_ context.Context, text string) {
			text = strings.TrimSpace(text)
			if text != "" {
				notices = append(notices, text)
			}
		}
		configs, err := hookbuiltin.RegisterAll(registrar, loadOpts)
		if err == nil {
			err = registerCronPlatformHook(registrar, cronService)
		}
		return hook.ReloadReport{Notices: notices}, configs, err
	}
	hookService := hookcontrol.New(hooks, hookRuntime, loadHooks)
	hookRuntime.SetPluginReloadPreparer(hookService.PreparePluginReload)
	report, err := hookService.HookReload()
	for _, notice := range report.Notices {
		notifyHookIssue(context.Background(), notice)
	}
	if err != nil {
		foundation.Logger.Error("hook registration failed", "error", err)
		notifyHookIssue(context.Background(), fmt.Sprintf("Hook 注册失败：%v", err))
	}

	return hookService
}

func buildAgent(
	foundation *FoundationComponents,
	models *modelmgr.Service,
	platforms PlatformComponents,
	toolRuntime *builtin.Runtime,
	securityPolicy *security.Policy,
	hooks *hook.DefaultManager,
	hookRuntime *hookruntime.Manager,
	hookService *hookcontrol.Service,
	dispatcher *dispatch.Router,
	notices *notification.Manager,
) (*agent.Agent, error) {
	cfg := foundation.Config
	definitions := append(platformbuiltin.ConfigDefinitions(), hookrules.ConfigDefinition())
	diagnostics, err := doctor.New(cfg.ConfigPath, config.NewInspector(definitions...))
	if err != nil {
		return nil, err
	}
	agt, err := agent.NewWithOptions(agent.Options{
		Doctor:                diagnostics,
		Platform:              platforms.Primary,
		Models:                models,
		Contexts:              contextmgr.New(contextmgr.Options{Store: foundation.Store, Models: models, Config: cfg.Context, Metadata: cfg.ModelMetadata, Providers: cfg.Providers}),
		ToolState:             toolrun.NewStateService(foundation.Store),
		Providers:             cfg.Providers,
		Store:                 foundation.Store,
		Media:                 toolRuntime.FileManager.Media,
		CommandPrefixes:       cfg.Commands.Prefixes,
		SessionConfig:         session.Config{NamingConfig: session.NamingConfig{TriggerStep: cfg.Session.Naming.TriggerStep}, DefaultMode: cfg.Session.DefaultMode},
		NamingNotifier:        namingLogger{logger: foundation.Logger},
		SoulPath:              cfg.Soul.Path,
		ResidentMemoryStore:   toolRuntime.ResidentMemoryStore,
		LLMRequestConfig:      cfg.LLMRequest,
		HookService:           hookService,
		HookManager:           hooks,
		HookRuntime:           hookRuntime,
		Dispatcher:            dispatcher,
		Notifications:         notices,
		Logs:                  foundation.Logs,
		ToolRegistry:          toolRuntime.Registry,
		FileRollback:          toolRuntime.FileRollback,
		Skills:                toolRuntime.SkillManager,
		SecurityPolicy:        securityPolicy,
		ContextConfig:         cfg.Context,
		ModelMetadata:         cfg.ModelMetadata,
		SessionListPageSize:   cfg.View.SessionListPageSize,
		CleanupRetentionDays:  cfg.Maintenance.SessionCleanup.RetentionDays,
		MediaRetentionDays:    cfg.Maintenance.SandboxCleanup.RetentionDays,
		SessionIdleExpiration: cfg.Session.IdleExpiration,
		SandboxRoot:           cfg.Sandbox.Root,
		ToolsConfig:           cfg.Tools,
		ToolTagsPath:          cfg.ToolTagsConfigPath,
		ToolTags:              cfg.ToolTags,
	})
	if err != nil {
		return nil, err
	}
	return agt, nil
}

func auditFunc(logs LogManager) func(string, ...any) {
	return func(event string, attrs ...any) {
		logs.Audit().Log(context.Background(), slog.LevelInfo, "audit event", append([]any{"event", event}, attrs...)...)
	}
}

type hookRuntimeLifecycle struct {
	runtime *hookruntime.Manager
}

func (l hookRuntimeLifecycle) Close(ctx context.Context) error {
	return l.runtime.Close(ctx)
}
