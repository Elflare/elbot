package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

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
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/runtimeinfo"
)

type defaultRuntimeFactory struct{}

func (defaultRuntimeFactory) Build(ctx context.Context, req RuntimeRequest) (*RuntimeComponents, error) {
	foundation := req.Foundation
	cfg := foundation.Config
	logger := foundation.Logger
	var agt *agent.Agent
	sendNotice := func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
		if agt == nil {
			return delivery.Receipt{}, fmt.Errorf("agent is not ready")
		}
		return agt.SendNotice(ctx, target, outputs)
	}

	cronService, err := buildCronService(ctx, foundation, sendNotice)
	if err != nil {
		return nil, err
	}

	toolRuntime, err := builtin.NewRuntime(builtin.RuntimeOptions{
		ConfigDir: filepath.Dir(cfg.ConfigPath),
		RuntimeInfo: runtimeinfo.Info{
			ConfigPath:   cfg.ConfigPath,
			SandboxRoot:  cfg.Sandbox.Root,
			FileDelivery: cfg.FileDelivery,
		},
		CronService:            cronService,
		ChatHistory:            foundation.ChatHistory,
		ResidentMemoryMaxUnits: resident.Limits{Core: cfg.ResidentMemory.CoreMaxUnits, Normal: cfg.ResidentMemory.NormalMaxUnits},
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

	startupHookNotices := []string{}
	notifyHookIssue := func(ctx context.Context, text string) {
		if agt == nil {
			startupHookNotices = append(startupHookNotices, text)
			return
		}
		_, _ = agt.SendNotice(ctx, delivery.Target{}, []delivery.Output{delivery.Text(text)})
	}

	hookRuntime := hookruntime.NewManager(hookruntime.Options{
		Registry:  toolRuntime.Registry,
		Logger:    logger,
		Audit:     auditFunc(foundation.Logs),
		Send:      sendNotice,
		SharedDir: filepath.Join(config.PluginConfigDir(cfg.ConfigPath), "_shared"),
	})

	hookService := buildHookService(foundation, req.Platforms, toolRuntime, cronService, hooks, hookRuntime, notifyHookIssue, sendNotice)
	req.Profiler.Mark("hook register")

	agt, err = buildAgent(foundation, req.Models, req.Platforms, toolRuntime, securityPolicy, hooks, hookRuntime, hookService)
	if err != nil {
		if closeErr := hookRuntime.Close(context.Background()); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup hook runtime after agent build: %w", closeErr))
		}
		return nil, err
	}
	cronService.SetRunner(agt)
	for _, notice := range startupHookNotices {
		notifyHookIssue(context.Background(), notice)
	}
	req.Profiler.Mark("agent init")

	return &RuntimeComponents{
		Agent:       agt,
		Handler:     agt,
		CronService: cronService,
		ElvenaBus:   elvenaBus,
		Lifecycle:   hookRuntimeLifecycle{runtime: hookRuntime},
	}, nil
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
		SendTarget: func(ctx context.Context, target delivery.Target, out delivery.Output) (delivery.Receipt, error) {
			return send(ctx, target, []delivery.Output{out})
		},
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
	models ModelClients,
	platforms PlatformComponents,
	toolRuntime *builtin.Runtime,
	securityPolicy *security.Policy,
	hooks *hook.DefaultManager,
	hookRuntime *hookruntime.Manager,
	hookService *hookcontrol.Service,
) (*agent.Agent, error) {
	cfg := foundation.Config
	agt, err := agent.NewWithOptions(agent.Options{
		Platform:              platforms.Primary,
		Clients:               models.ByProvider,
		ModeModels:            cfg.ModeModels,
		Providers:             cfg.Providers,
		StatePath:             cfg.StateConfigPath,
		Store:                 foundation.Store,
		CommandPrefixes:       cfg.Commands.Prefixes,
		SessionConfig:         session.Config{NamingConfig: session.NamingConfig{TriggerStep: cfg.Session.Naming.TriggerStep}, DefaultMode: cfg.Session.DefaultMode},
		NamingSelection:       cfg.NamingModel,
		NamingNotifier:        namingLogger{logger: foundation.Logger},
		SoulPath:              cfg.Soul.Path,
		ResidentMemoryStore:   toolRuntime.ResidentMemoryStore,
		LLMRequestConfig:      cfg.LLMRequest,
		HookService:           hookService,
		HookManager:           hooks,
		HookRuntime:           hookRuntime,
		OutputManager:         delivery.NewManager(nil, foundation.Logger),
		Logs:                  foundation.Logs,
		ToolRegistry:          toolRuntime.Registry,
		Skills:                toolRuntime.SkillManager,
		SecurityPolicy:        securityPolicy,
		ContextConfig:         cfg.Context,
		ModelMetadata:         cfg.ModelMetadata,
		CompactModel:          cfg.CompactModel,
		SessionListPageSize:   cfg.View.SessionListPageSize,
		CleanupRetentionDays:  cfg.Maintenance.SessionCleanup.RetentionDays,
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
