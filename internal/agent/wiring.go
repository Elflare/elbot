package agent

import (
	"context"
	"elbot/internal/modelmgr"
	"elbot/internal/notification/rules"
	"elbot/internal/workspace"
	"fmt"
	"time"

	chatroute "elbot/internal/agent/chat"
	agentconfirmation "elbot/internal/agent/confirmation"
	"elbot/internal/agent/dialogue"
	agentexecution "elbot/internal/agent/execution"
	agenthooks "elbot/internal/agent/hooks"
	agentinput "elbot/internal/agent/input"
	agentoutput "elbot/internal/agent/output"
	responseroute "elbot/internal/agent/responses"
	"elbot/internal/agent/routes"
	agentshare "elbot/internal/agent/share"
	agenttools "elbot/internal/agent/tools"
	"elbot/internal/command"
	commandbuiltin "elbot/internal/command/builtin"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Supervisor{ctx: ctx, cancel: cancel, signals: newSignals(), bindings: &signalBindings{}, done: make(chan struct{})}
	m.routes = routes.New()
	m.sessions = session.NewServiceWithConfig(deps.Store, session.Config{NamingConfig: session.NamingConfig{TriggerStep: cfg.Session.Naming.TriggerStep}, DefaultMode: cfg.Session.DefaultMode}, session.NewTitleGenerator(deps.Models))
	m.requests = request.NewManager(0)
	m.turns = turn.NewManager()
	m.contexts = contextmgr.New(contextmgr.Options{Compactors: m.routes, Store: deps.Store, Models: deps.Models, Config: cfg.Context})
	m.commands = command.NewRouter(cfg.Commands.Prefixes)
	sessionCommands := commandbuiltin.NewSessionCommandState(cfg.View.SessionListPageSize, cfg.Maintenance.SessionCleanup.RetentionDays)
	signals := m.signals
	view := dialogue.ExecutionView{Sessions: deps.Store.Sessions()}
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: deps.Platform.PrimaryName(), ActorID: "cli:local", ScopeID: "local", Permission: deps.Permission})
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{
		Identity: identity,
	})
	m.bridge = bridge
	status := agentexecution.NewStatusRecorder(agentexecution.StatusRecorderDeps{View: view, Turns: m.turns, Changed: signals.StatusChanged})
	output := agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{Dispatcher: deps.Platform, Hooks: bridge, Reasoning: deps.Platform.StartReasoning})
	policy := agentconfirmation.NewConfirmationPolicy(agentconfirmation.ConfirmationPolicyDeps{Identity: identity, IdleExpiration: sessionIdleExpirationConfig(cfg.Session.IdleExpiration), UserConfirmationTimeout: 10 * time.Minute})
	confirmations := agentconfirmation.NewConfirmationCoordinator(agentconfirmation.ConfirmationCoordinatorDeps{
		Sessions: m.sessions,
		Requests: m.requests,
		Turns:    m.turns,
		Commands: m.commands,
		Identity: identity,
		Output:   output,
		Policy:   policy,
	})
	toolRuntime := agenttools.NewToolRuntimeState(agenttools.ToolRuntimeStateDeps{Manager: deps.Tools, FileRollback: deps.Tools, Identity: identity})
	toolDeps := agenttools.NewToolRunDeps(agenttools.ToolRunDeps{
		NewWorkspaceStore: func(id string) workspace.WorkspaceStore {
			return session.NewWorkspaceStore(m.sessions, deps.Store.Sessions(), id)
		},
		Status:        status,
		Hooks:         bridge,
		Requests:      m.requests,
		Turns:         m.turns,
		Identity:      identity,
		Runtime:       toolRuntime,
		Sessions:      m.sessions,
		Confirmations: confirmations,
		View:          view,
	})
	runner := dialogue.NewRunner(dialogue.RunnerDeps{
		Status:       status,
		Routes:       m.routes,
		Dialogues:    deps.Store.Dialogues(),
		Sessions:     m.sessions,
		Turns:        m.turns,
		Contexts:     m.contexts,
		Media:        deps.Media,
		Identity:     identity,
		Hooks:        bridge,
		Output:       output,
		ToolManager:  deps.Tools,
		ToolDeps:     toolDeps,
		ToolProvider: toolRuntime.Provider(),
		MaxRounds:    cfg.Tools.MaxRoundsPerTurn,
		SoulPath:     cfg.Soul.Path,
		Memory:       deps.Tools,
		Preloader:    deps.Tools,
		Completed:    signals.ModelCallCompleted,
		Vision:       signals.VisionFallbackUsed,
		View:         view,
	})
	chat := chatroute.NewLoop(chatroute.LoopDeps{
		Identity: identity,
		SelectionForTurn: func(ctx context.Context, row *storage.Session) modelmgr.Selection {
			return modelmgr.SelectionForTurn(ctx, deps.Models, row)
		},
		NewToolTranscriptCommitter: func() agentshare.ToolTranscriptCommitter {
			return &dialogue.ToolTranscriptCommitter{Messages: runner.Messages}
		},
		ToolCalls: deps.Store.ToolCalls(),
		Loader:    contextmgr.Loader{Store: deps.Store},
		Status:    status,
		Contexts:  m.contexts,
		Models:    deps.Models,
		Turns:     m.turns,
		View:      view,
		Preparer:  runner.Preparer,
		Tools:     runner.Preparer.Tools,
		Messages:  runner.Messages,
		Calls:     runner.Calls,
		System:    runner.System,
	})
	response := responseroute.NewLoop(responseroute.LoopDeps{
		Identity:    identity,
		Gate:        runner.Messages.Gate,
		SessionRows: deps.Store.Sessions(),
		SelectionForTurn: func(ctx context.Context, row *storage.Session) modelmgr.Selection {
			return modelmgr.SelectionForTurn(ctx, deps.Models, row)
		},
		Status:     status,
		Repository: deps.Store.Dialogues(),
		Turns:      m.turns,
		View:       view,
		Preparer:   runner.Preparer,
		Tools:      runner.Preparer.Tools,
		Messages:   runner.Messages,
		Calls:      runner.Calls,
		System:     runner.System,
	})
	bindProtocolRoutes(m.routes, chat, response)
	m.sessions.SetMaterials(m.routes)
	if err := m.contexts.CheckCompaction(llm.Origin{APIType: llm.APITypeChat}); err != nil {
		return m, fmt.Errorf("context compaction wiring: %w", err)
	}
	m.sessions.SetForegroundCheck(func(_ context.Context, source *storage.Session) error {
		return view.CheckSelection(source, deps.Models.ResolveMode(storage.SessionModeWork))
	})
	execution := agentexecution.NewExecutionCoordinator(ctx, agentexecution.ExecutionCoordinatorDeps{
		Sessions:    m.sessions,
		SessionRows: deps.Store.Sessions(),
		Turns:       m.turns,
		Requests:    m.requests,
		Contexts:    m.contexts,
		SelectionForTurn: func(ctx context.Context, row *storage.Session) modelmgr.Selection {
			return modelmgr.SelectionForTurn(ctx, deps.Models, row)
		},
		TurnTimeout:     rules.TurnTimeout,
		Dialogue:        runner,
		Identity:        identity,
		View:            view,
		Output:          output,
		Status:          status,
		WaitPolicy:      policy,
		ResponseTimeout: time.Duration(cfg.LLMRequest.ResponseTimeoutSeconds) * time.Second,
	})
	m.execution = execution
	m.sessions.SetActivitySource(func() []string {
		var ids []string
		for _, active := range m.turns.SnapshotAll() {
			if active.Phase != turn.PhaseIdle {
				ids = append(ids, active.SessionID)
			}
		}
		return ids
	})
	input := agentinput.NewInputCoordinator(agentinput.InputCoordinatorDeps{
		NewWorkspaceStore: func(id string) workspace.WorkspaceStore {
			return session.NewWorkspaceStore(m.sessions, deps.Store.Sessions(), id)
		},
		Sessions:      m.sessions,
		SessionRows:   deps.Store.Sessions(),
		Turns:         m.turns,
		Identity:      identity,
		Hooks:         bridge,
		Output:        output,
		Execution:     execution,
		Confirmations: confirmations,
		Preloader:     deps.Tools,
		ToolState:     deps.Tools,
		WaitPolicy:    policy,
	})
	fileCommands := agentinput.NewFileCommandPreparer(agentinput.FileCommandPreparerDeps{
		NewWorkspaceStore: func(id string) workspace.WorkspaceStore {
			return session.NewWorkspaceStore(m.sessions, deps.Store.Sessions(), id)
		},
		Service:  deps.Tools,
		Sessions: m.sessions,
		Turns:    m.turns,
		Requests: m.requests,
		Identity: identity,
	})
	commands, err := wireCommands(agentinput.CommandExecutorDeps{
		IsRiskConfirmationCommand: func(text string) bool { return agentconfirmation.IsRiskConfirmationCommand(text, m.commands) },
		Router:                    m.commands,
		Sessions:                  m.sessions,
		Turns:                     m.turns,
		Identity:                  identity,
		Execution:                 execution,
		Output:                    output,
		Confirmations:             confirmations,
		Input:                     input,
	}, commandbuiltin.Deps{
		Router:             m.commands,
		Sessions:           m.sessions,
		Requests:           m.requests,
		Turns:              m.turns,
		Store:              deps.Store,
		Scope:              identity.Scope,
		Models:             deps.Models,
		Contexts:           m.contexts,
		Compact:            execution,
		Tools:              deps.Tools,
		Skills:             deps.Tools,
		FileRollback:       fileRollbackCapabilities{deps.Tools},
		PrepareFileContext: fileCommands.PrepareFileCommand,
		Hooks:              deps.Hooks,
		SessionState:       sessionCommands,
		Logs:               logging.Reader{Dir: deps.Logs.LogDir()},
		RuntimeStatus:      status.Snapshot,
		Usage:              status.Usage,
	})
	if err != nil {
		return m, err
	}
	handler := agentinput.NewMessageHandler(agentinput.MessageHandlerDeps{
		History:   deps.Media.History,
		Identity:  identity,
		Hooks:     bridge,
		Output:    output,
		Commands:  commands,
		Input:     input,
		Media:     deps.Media,
		Messages:  deps.Store.Messages(),
		MediaRows: deps.Store.Media(),
	})
	m.completion = completion.NewService(
		completion.RiskConfirmationSource{Router: m.commands, Sessions: m.sessions, Turns: m.turns, Scope: identity.Scope, CommandNames: agentconfirmation.RiskConfirmationCommandNames()},
		completion.ForkMessageSource{Router: m.commands, Sessions: m.sessions, Store: deps.Store, Scope: identity.Scope},
		completion.ToolDirectiveSource{
			Context: func(ctx context.Context) (context.Context, error) {
				row, err := m.sessions.Current(ctx, identity.Scope(ctx))
				if err != nil && err != storage.ErrNotFound {
					return ctx, err
				}
				return execution.ModelContext(ctx, row)
			},
			// TODO: Completion 的公开查询契约尚需改用 Tool 根部类型。
			Registry:   func() completion.ToolCapabilities { return deps.Tools },
			Actor:      identity.Actor,
			Permission: func() *security.Permission { return deps.Permission },
			Tags: func(ctx context.Context, _ completion.ToolCapabilities, actor contextinfo.Actor, _ *security.Permission) ([]string, error) {
				return deps.Tools.ToolTags(contextinfo.WithActor(ctx, actor))
			},
			ToolNamesByTag: func(ctx context.Context, _ completion.ToolCapabilities, tag string, query tool.ToolQuery) ([]string, error) {
				return deps.Tools.ToolNamesByTag(ctx, tag, query)
			},
		},
		completion.RouterSource{Router: m.commands, Actor: identity.Actor},
	)
	bindings := m.bindings
	if err := bindings.connectAgentNotifications(signals); err != nil {
		return m, err
	}
	if err := bindings.connectSession(m.sessions, deps.Tools, execution); err != nil {
		return m, err
	}
	if err := m.bindings.connectModels(deps.Models); err != nil {
		return m, err
	}
	m.handler = handler
	return m, nil
}

func sessionIdleExpirationConfig(cfg config.SessionIdleExpirationConfig) session.IdleExpirationConfig {
	return session.IdleExpirationConfig{
		GroupUserTTLMinutes:         cfg.GroupUserTTLMinutes,
		GroupSuperadminTTLMinutes:   cfg.GroupSuperadminTTLMinutes,
		PrivateUserTTLMinutes:       cfg.PrivateUserTTLMinutes,
		PrivateSuperadminTTLMinutes: cfg.PrivateSuperadminTTLMinutes,
	}
}

// wireCommands constructs the command consumer and registers its modules.
func wireCommands(deps agentinput.CommandExecutorDeps, modules commandbuiltin.Deps) (*agentinput.CommandExecutor, error) {
	executor := agentinput.NewCommandExecutor(deps)
	if err := commandbuiltin.RegisterDefaultModules(modules.Router, modules); err != nil {
		return nil, err
	}
	return executor, nil
}

// fileRollbackCapabilities adapts the command's List name to Tool's rollback operation.
type fileRollbackCapabilities struct{ ToolCapabilities }

func (c fileRollbackCapabilities) List(ctx context.Context) ([]fileops.RollbackInfo, error) {
	return c.ListRollbacks(ctx)
}

func bindProtocolRoutes(registry *routes.Registry, chat *chatroute.Loop, response *responseroute.Loop) {
	registry.Register(routes.Binding{APIType: llm.APITypeChat, Loop: chat, Compactor: chat.Compactor(), Material: chat.Material()})
	registry.Register(routes.Binding{APIType: llm.APITypeResponse, Loop: response, Compactor: response.Compactor(), Material: response.Material()})
}
