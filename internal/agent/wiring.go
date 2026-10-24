package agent

import (
	"context"
	"fmt"
	"time"

	chatroute "elbot/internal/agent/chat"
	agentconfirmation "elbot/internal/agent/confirmation"
	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
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
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Supervisor{ctx: ctx, cancel: cancel, signals: agentevents.NewSignals(), bindings: &signalBindings{}, done: make(chan struct{})}
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
		Status:        status,
		Hooks:         bridge,
		Requests:      m.requests,
		Turns:         m.turns,
		Identity:      identity,
		Runtime:       toolRuntime,
		Sessions:      m.sessions,
		SessionRows:   deps.Store.Sessions(),
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
		Signals:      signals,
		View:         view,
	})
	chat := chatroute.NewLoop(chatroute.LoopDeps{
		Status:   status,
		Store:    deps.Store,
		Contexts: m.contexts,
		Models:   deps.Models,
		Turns:    m.turns,
		View:     view,
		Preparer: runner.Preparer,
		Tools:    runner.Preparer.Tools,
		Messages: runner.Messages,
		Calls:    runner.Calls,
		System:   runner.System,
	})
	response := responseroute.NewLoop(responseroute.LoopDeps{
		Status:     status,
		Repository: deps.Store.Dialogues(),
		Models:     deps.Models,
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
	execution := wireExecution(ctx, agentexecution.ExecutionCoordinatorDeps{
		Sessions:        m.sessions,
		SessionRows:     deps.Store.Sessions(),
		Turns:           m.turns,
		Requests:        m.requests,
		Contexts:        m.contexts,
		Models:          deps.Models,
		Dialogue:        runner,
		Identity:        identity,
		View:            view,
		Output:          output,
		Status:          status,
		WaitPolicy:      policy,
		ResponseTimeout: time.Duration(cfg.LLMRequest.ResponseTimeoutSeconds) * time.Second,
	})
	m.execution = execution
	input := agentinput.NewInputCoordinator(agentinput.InputCoordinatorDeps{
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
		Service:     deps.Tools,
		Sessions:    m.sessions,
		SessionRows: deps.Store.Sessions(),
		Turns:       m.turns,
		Requests:    m.requests,
		Identity:    identity,
	})
	commands, err := wireCommands(agentinput.CommandExecutorDeps{
		Router:        m.commands,
		Sessions:      m.sessions,
		Turns:         m.turns,
		Identity:      identity,
		Execution:     execution,
		Output:        output,
		Confirmations: confirmations,
		Input:         input,
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
		Identity:  identity,
		Hooks:     bridge,
		Output:    output,
		Commands:  commands,
		Input:     input,
		Media:     deps.Media,
		Messages:  deps.Store.Messages(),
		MediaRows: deps.Store.Media(),
	})
	completion := agentinput.NewCompletion(agentinput.CompletionDeps{
		Commands:       m.commands,
		Sessions:       m.sessions,
		Turns:          m.turns,
		Store:          deps.Store,
		Identity:       identity,
		Registry:       deps.Tools,
		Tags:           deps.Tools.ToolTags,
		ToolNamesByTag: deps.Tools.ToolNamesByTag,
		Execution:      execution,
	})
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
	m.handler, m.completion = handler, completion
	return m, nil
}

// wireExecution installs synchronous participants on this assembly's private Sessions.
func wireExecution(ctx context.Context, deps agentexecution.ExecutionCoordinatorDeps) *agentexecution.ExecutionCoordinator {
	execution := agentexecution.NewExecutionCoordinator(ctx, deps)
	deps.Sessions.SetActivitySource(func() []string {
		var ids []string
		for _, active := range deps.Turns.SnapshotAll() {
			if active.Phase != turn.PhaseIdle {
				ids = append(ids, active.SessionID)
			}
		}
		return ids
	})
	return execution
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
	if err := commandbuiltin.RegisterDefaultModules(deps.Router, modules); err != nil {
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
