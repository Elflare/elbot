package app

import (
	"context"
	"path/filepath"
	"time"

	"elbot/internal/agent"
	"elbot/internal/command"
	commandbuiltin "elbot/internal/command/builtin"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/doctor"
	"elbot/internal/fileops"
	"elbot/internal/hook"
	hookcontrol "elbot/internal/hook/control"
	hookrules "elbot/internal/hook/rules"
	"elbot/internal/logging"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/notification"
	notificationrules "elbot/internal/notification/rules"
	platformbuiltin "elbot/internal/platform/builtin"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/session"
	toolbuiltin "elbot/internal/tool/builtin"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

// sharedServices contains the single instances wired into all runtime consumers.
// It owns no background workers; their lifetimes belong to runtimeLifecycle.
type sharedServices struct {
	Models          *modelmgr.Service
	Contexts        *contextmgr.Service
	ToolState       *toolrun.StateService
	ToolPreloader   *toolrun.PreloadService
	Sessions        *session.Service
	Requests        *request.Manager
	Turns           *turn.Manager
	Commands        *command.Router
	SessionCommands *commandbuiltin.SessionCommandState
	Files           *fileops.Service
	Policy          *security.Policy
	Media           *media.Manager
	Dispatcher      *dispatch.Router
	Notifications   *notification.Manager
	Doctor          *doctor.Service
}

func buildSharedServices(ctx context.Context, req RuntimeRequest) (*sharedServices, error) {
	foundation, cfg := req.Foundation, req.Foundation.Config
	logger := foundation.Logger
	models, err := modelmgr.New(modelmgr.Options{
		Clients: req.Models.ByProvider, Providers: cfg.Providers, ModeModels: cfg.ModeModels,
		CompactModel: cfg.CompactModel, NamingModel: cfg.NamingModel,
		StatePath: cfg.StateConfigPath, DefaultMode: cfg.Session.DefaultMode,
	})
	if err != nil {
		return nil, err
	}
	credentials, err := resolveFileDeliveryCredentials(cfg.FileDelivery, filepath.Dir(cfg.ConfigPath))
	if err != nil {
		logger.Warn("S3 media backend is unavailable; remote operations will fail until configuration is fixed", "error", err)
		credentials = nil
	}
	mediaCenter, err := media.NewConfigured(ctx, foundation.Store, filepath.Join(filepath.Dir(cfg.Sandbox.Root), "media"), cfg.FileDelivery, credentials)
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
	mediaCenter.Media, mediaCenter.Logger = cfg.Media, logger

	dispatcher := dispatch.New(dispatch.Options{Primary: req.Platforms.Primary, Store: foundation.Store, Media: mediaCenter, MediaRetentionDays: cfg.Maintenance.SandboxCleanup.RetentionDays, Logger: logger})
	for _, adapter := range req.Platforms.Runtimes {
		if adapter != nil {
			dispatcher.RegisterPlatformSender(adapter.Name(), adapter)
		}
	}
	notices := notification.New(dispatcher, logger, req.Platforms.Primary != nil && req.Platforms.Primary.Name() == "service")
	models.SetRetryNotifier(notificationrules.ModelRetry(notices))
	definitions := append(platformbuiltin.ConfigDefinitions(), hookrules.ConfigDefinition())
	diagnostics, err := doctor.New(cfg.ConfigPath, config.NewInspector(definitions...))
	if err != nil {
		return nil, err
	}
	s := &sharedServices{
		Models:    models,
		Contexts:  contextmgr.New(contextmgr.Options{Store: foundation.Store, Models: models, Config: cfg.Context, Metadata: cfg.ModelMetadata, Providers: cfg.Providers}),
		ToolState: toolrun.NewStateService(foundation.Store),
		Sessions:  session.NewServiceWithConfig(foundation.Store, session.Config{NamingConfig: session.NamingConfig{TriggerStep: cfg.Session.Naming.TriggerStep}, DefaultMode: cfg.Session.DefaultMode}, session.NewTitleGenerator(models), namingLogger{logger: logger}),
		Requests:  request.NewManager(0), Turns: turn.NewManager(),
		Commands:        command.NewRouter(cfg.Commands.Prefixes),
		SessionCommands: commandbuiltin.NewSessionCommandState(cfg.View.SessionListPageSize, cfg.Maintenance.SessionCleanup.RetentionDays),
		Files:           fileops.NewService(nil),
		Policy:          security.NewPolicy(cfg.Security.UserMaxToolRisk, cfg.Security.SuperadminConfirmRisk, cfg.Security.Superadmins),
		Media:           mediaCenter, Dispatcher: dispatcher, Notifications: notices, Doctor: diagnostics,
	}
	if foundation.Maintenance != nil {
		foundation.Maintenance.Media = s.Media
		foundation.Maintenance.Sessions = s.Sessions
	}
	return s, nil
}

func registerBuiltinCommands(foundation *FoundationComponents, s *sharedServices, agt *agent.Agent, tools *toolbuiltin.Runtime, hooks *hookcontrol.Service) error {
	return commandbuiltin.RegisterDefaultModules(s.Commands, commandbuiltin.Deps{
		Doctor: s.Doctor, Router: s.Commands, Sessions: s.Sessions,
		Requests: s.Requests, Turns: s.Turns, Store: foundation.Store, Scope: agt.Scope,
		Models: s.Models, Contexts: s.Contexts, Logger: foundation.Logger, Compact: agt,
		Tools: tools.Registry, Skills: tools.SkillManager,
		FileRollback: s.Files, PrepareFileContext: agt.PrepareFileCommand,
		Hooks: hooks, SessionState: s.SessionCommands, Audit: auditFunc(foundation.Logs),
		Logs: logging.Reader{Dir: foundation.Logs.LogDir()}, RuntimeStatus: agt.RuntimeStatus,
	})
}

// Execution participants run synchronously under their owners' contracts;
// lifecycle notifications continue to use the separately owned signal bindings.
func bindAgentExecution(s *sharedServices, agt *agent.Agent, hooks *hook.DefaultManager) {
	s.Sessions.SetForegroundActivation(agt.AdoptForeground)
	s.Sessions.SetActivitySource(func() []string {
		var ids []string
		for _, active := range s.Turns.SnapshotAll() {
			if active.Phase != turn.PhaseIdle {
				ids = append(ids, active.SessionID)
			}
		}
		return ids
	})
	hooks.SetWakeupFunc(agt.HookWakeup)
	hooks.SetObserver(agt.ObserveHookRun)
}
