package app

import (
	"context"
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
	elcron "elbot/internal/cron"
	"elbot/internal/delivery"
	"elbot/internal/elvena"
	"elbot/internal/hook"
	hookbuiltin "elbot/internal/hook/builtin"
	hookcontrol "elbot/internal/hook/control"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/processenv"
	"elbot/internal/session"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/runtimeinfo"
	"elbot/internal/toolrun"
)

type defaultRuntimeFactory struct{}

func (defaultRuntimeFactory) Build(ctx context.Context, req RuntimeRequest) (*RuntimeComponents, error) {
	ctx, cancel := context.WithCancel(ctx)
	lifecycle := &runtimeLifecycle{cancel: cancel}
	components := &RuntimeComponents{Lifecycle: lifecycle, Signals: &signalBindings{}}
	foundation := req.Foundation
	cfg := foundation.Config
	logger := foundation.Logger
	services, err := buildSharedServices(ctx, req)
	if err != nil {
		return components, err
	}
	lifecycle.sessions = services.Sessions
	if err := components.Signals.connectNaming(services.Sessions, logger); err != nil {
		return components, err
	}
	if err := components.Signals.connectModels(services.Models, services.Notifications, logger); err != nil {
		return components, err
	}
	services.Sessions.StartNaming(ctx)
	dotEnv, err := config.LoadDotEnv(filepath.Dir(cfg.ConfigPath))
	if err != nil {
		return components, fmt.Errorf("load process environment: %w", err)
	}
	baseProcessEnv := processenv.New(os.Environ())
	shellProcessEnv := baseProcessEnv.Fill(dotEnv)
	hookProcessEnv := hook.ProcessEnvironment(baseProcessEnv)
	sendNotice := func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
		return services.Dispatcher.SendNotice(ctx, delivery.Notice{Target: target, Outputs: outputs})
	}
	cronService, err := buildCronService(ctx, foundation, services.Models, services.Sessions, sendNotice)
	if err != nil {
		return components, err
	}
	toolRuntime, err := builtin.NewRuntime(builtin.RuntimeOptions{
		FileRollback: services.Files,
		ConfigDir:    filepath.Dir(cfg.ConfigPath),
		RuntimeInfo: runtimeinfo.Info{
			ConfigPath:   cfg.ConfigPath,
			SandboxRoot:  cfg.Sandbox.Root,
			FileDelivery: cfg.FileDelivery,
		},
		CronService:            cronService,
		ChatHistory:            foundation.ChatHistory,
		Store:                  foundation.Store,
		Media:                  services.Media,
		ResidentMemoryMaxUnits: resident.Limits{Core: cfg.ResidentMemory.CoreMaxUnits, Normal: cfg.ResidentMemory.NormalMaxUnits},
		ProcessEnv:             shellProcessEnv,
	})
	if err != nil {
		return components, err
	}
	req.Profiler.Mark("builtin tools register")
	lifecycle.skillDone = toolRuntime.SkillManager.StartDelayedReload(ctx, time.Second)
	req.Profiler.Mark("skill reload scheduled")

	hooks := hook.NewManager()
	hooks.SetLogger(logger)
	elvenaBus := elvena.NewBus()

	notifyHookIssue := func(ctx context.Context, text string) {
		services.Notifications.Text(ctx, slog.LevelWarn, text)
	}

	hookRuntime := hookruntime.NewManager(hookruntime.Options{
		Media:      services.Media,
		Registry:   toolRuntime.Registry,
		Logger:     logger,
		Audit:      auditFunc(foundation.Logs),
		Send:       sendNotice,
		SharedDir:  filepath.Join(config.PluginConfigDir(cfg.ConfigPath), "_shared"),
		ProcessEnv: hookProcessEnv,
	})

	lifecycle.hooks = hookRuntime
	hookService := buildHookService(foundation, req.Platforms, toolRuntime, hooks, hookRuntime, hookProcessEnv, notifyHookIssue, sendNotice)
	req.Profiler.Mark("hook register")

	agt, err := buildAgent(foundation, req.Platforms, services, toolRuntime, hooks, hookRuntime)
	if err != nil {
		return components, err
	}
	if err := registerBuiltinCommands(foundation, services, agt, toolRuntime, hookService); err != nil {
		return components, err
	}
	cronService.SetRunner(agt)
	req.Profiler.Mark("agent init")

	bindings := components.Signals
	if err := bindings.connectAgentLogs(agt.Signals(), logger, foundation.Logs.Audit()); err != nil {
		return components, err
	}
	if err := bindings.connectAgentNotifications(agt.Signals(), services.Notifications, agt.NotificationSender(), logger); err != nil {
		return components, err
	}
	if err := bindings.connectStatus(agt.Signals(), services.Sessions, services.Dispatcher, logger); err != nil {
		return components, err
	}
	if err := bindings.connectSession(services.Sessions, toolRuntime.FileRollback.Manager, foundation.Logger); err != nil {
		return components, err
	}
	*components = RuntimeComponents{
		Commands:      services.Commands,
		Dispatcher:    services.Dispatcher,
		Notifications: services.Notifications,
		Models:        services.Models,
		ToolPreloader: services.ToolPreloader,
		Signals:       bindings,
		Media:         services.Media,
		Agent:         agt,
		Handler:       agt,
		CronService:   cronService,
		ElvenaBus:     elvenaBus,
		Lifecycle:     lifecycle,
	}
	return components, nil
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

func buildCronService(ctx context.Context, foundation *FoundationComponents, models *modelmgr.Service, sessions *session.Service, send func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)) (*elcron.Service, error) {
	cfg := foundation.Config
	service := elcron.NewService(elcron.Options{
		Manager:          foundation.CronManager,
		Store:            foundation.Store,
		Logger:           foundation.Logger,
		EnabledPlatforms: enabledCronPlatforms(cfg),
		SandboxRoot:      cfg.Sandbox.Root,
		Audit:            auditFunc(foundation.Logs),
		SendTarget:       send,
		Models:           models,
		Sessions:         sessions,
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

func buildAgent(foundation *FoundationComponents, platforms PlatformComponents, services *sharedServices, tools *builtin.Runtime, hooks *hook.DefaultManager, hookRuntime *hookruntime.Manager) (*agent.Agent, error) {
	cfg := foundation.Config
	runner := toolrun.NewManager(tools.Registry, services.Policy)
	runner.Media = services.Media
	preloader := toolrun.NewPreloadService(toolrun.PreloadOptions{Registry: tools.Registry, TagsPath: cfg.ToolTagsConfigPath, Tags: cfg.ToolTags, Audit: auditFunc(foundation.Logs)})
	services.ToolPreloader = preloader
	agt, err := agent.NewWithOptions(agent.Options{
		Platform: platforms.Primary, Models: services.Models,
		Contexts: services.Contexts, ToolState: services.ToolState,
		Sessions: services.Sessions, Requests: services.Requests, Turns: services.Turns, Commands: services.Commands,
		Store: foundation.Store, Media: services.Media,
		SoulPath: cfg.Soul.Path, ResidentMemoryStore: tools.ResidentMemoryStore,
		LLMRequestConfig: cfg.LLMRequest, HookManager: hooks, HookRuntime: hookRuntime,
		Dispatcher: services.Dispatcher, Notifications: services.Notifications,
		Logs: foundation.Logs, ToolRegistry: tools.Registry, ToolRunner: runner, ToolPreloader: preloader, FileRollback: services.Files,
		SecurityPolicy: services.Policy, SessionIdleExpiration: cfg.Session.IdleExpiration,
		SandboxRoot: cfg.Sandbox.Root, ToolsConfig: cfg.Tools,
	})
	if err != nil {
		return nil, err
	}
	bindAgentExecution(services, agt, hooks)
	return agt, nil
}

func auditFunc(logs LogManager) func(string, ...any) {
	return func(event string, attrs ...any) {
		logs.Audit().Log(context.Background(), slog.LevelInfo, "audit event", append([]any{"event", event}, attrs...)...)
	}
}
