package module

import (
	"context"
	"fmt"
	"sync"
	"time"

	"elbot/internal/agent"
	chatroute "elbot/internal/agent/chat"
	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	responseroute "elbot/internal/agent/responses"
	"elbot/internal/agent/routes"
	"elbot/internal/command"
	commandbuiltin "elbot/internal/command/builtin"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// Module owns the foreground business line and its independently managed Session, Request and Turn state.
type Module struct {
	statusChanged   *signal.Signal[agentevents.StatusChangedEvent, signal.Empty]
	ctx             context.Context
	cancel          context.CancelFunc
	sessions        *session.Service
	requests        *request.Manager
	turns           *turn.Manager
	routes          *routes.Registry
	contexts        *contextmgr.Service
	commands        *command.Router
	handler         *agent.MessageHandler
	completion      *completion.Service
	execution       *agent.ExecutionCoordinator
	identity        *agent.IdentityResolver
	bridge          *agent.HookBridge
	bindings        *signalBindings
	mu              sync.Mutex
	started, closed bool
	done            chan struct{}
}

func NewModule(ctx context.Context, deps ModuleDeps) (*Module, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Module{ctx: ctx, cancel: cancel, bindings: &signalBindings{}, done: make(chan struct{})}
	m.routes = routes.New()
	m.sessions = session.NewServiceWithConfig(deps.Store, session.Config{NamingConfig: session.NamingConfig{TriggerStep: deps.Session.Naming.TriggerStep}, DefaultMode: deps.Session.DefaultMode}, session.NewTitleGenerator(deps.Models))
	m.requests = request.NewManager(0)
	m.turns = turn.NewManager()
	m.contexts = contextmgr.New(contextmgr.Options{Compactors: m.routes, Store: deps.Store, Models: deps.Models, Config: deps.Context})
	m.commands = command.NewRouter(deps.Prefixes)
	sessionCommands := commandbuiltin.NewSessionCommandState(deps.PageSize, deps.RetentionDays)
	signals := agentevents.NewSignals()
	m.statusChanged = signals.StatusChanged
	view := dialogue.ExecutionView{Sessions: deps.Store.Sessions()}
	identity := agent.NewIdentityResolver(agent.IdentityResolverDeps{PlatformName: deps.PlatformName, ActorID: "cli:local", ScopeID: "local", Policy: deps.Policy})
	bridge := agent.NewHookBridge(agent.HookBridgeDeps{
		Manager:    deps.HookRunner,
		Router:     deps.HookRouter,
		Requests:   m.requests,
		Identity:   identity,
		Media:      deps.Media,
		Failed:     signals.HookFailed,
		Dispatcher: deps.Dispatcher,
	})
	m.bridge = bridge
	status := agent.NewStatusRecorder(agent.StatusRecorderDeps{View: view, Turns: m.turns, Changed: signals.StatusChanged})
	output := agent.NewOutputSender(agent.OutputSenderDeps{Dispatcher: deps.Dispatcher, Hooks: bridge, Reasoning: deps.Reasoning})
	policy := agent.NewConfirmationPolicy(agent.ConfirmationPolicyDeps{Identity: identity, IdleExpiration: sessionIdleExpirationConfig(deps.Session.IdleExpiration), UserConfirmationTimeout: 10 * time.Minute})
	confirmations := agent.NewConfirmationCoordinator(agent.ConfirmationCoordinatorDeps{
		Sessions: m.sessions,
		Requests: m.requests,
		Turns:    m.turns,
		Commands: m.commands,
		Identity: identity,
		Output:   output,
		Policy:   policy,
	})
	toolRuntime := agent.NewToolRuntimeState(agent.ToolRuntimeStateDeps{Provider: nil, Manager: deps.ToolManager, Registry: deps.Registry, FileRollback: deps.Files, Identity: identity})
	toolDeps := agent.NewToolRunDeps(agent.ToolRunDeps{
		Status:        status,
		Hooks:         bridge,
		Requests:      m.requests,
		Turns:         m.turns,
		Identity:      identity,
		State:         deps.ToolState,
		Runtime:       toolRuntime,
		Sessions:      m.sessions,
		SessionRows:   deps.Store.Sessions(),
		ToolCalls:     deps.Store.ToolCalls(),
		Confirmations: confirmations,
		View:          view,
	})
	runner := dialogue.NewRunner(dialogue.RunnerDeps{
		Status:          status,
		Routes:          m.routes,
		Dialogues:       deps.Store.Dialogues(),
		Sessions:        m.sessions,
		Turns:           m.turns,
		Contexts:        m.contexts,
		Media:           deps.Media,
		Identity:        identity,
		Hooks:           bridge,
		Output:          output,
		ToolManager:     deps.ToolManager,
		ToolRegistry:    deps.Registry,
		ToolState:       deps.ToolState,
		ToolDeps:        toolDeps,
		ToolProvider:    toolRuntime.Provider(),
		DefaultProvider: toolRuntime.DefaultProvider(),
		MaxRounds:       deps.MaxRounds,
		SoulPath:        deps.SoulPath,
		Memory:          deps.Memory,
		Preloader:       deps.Preloader,
		Signals:         signals,
		View:            view,
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
	execution := wireExecution(ctx, agent.ExecutionCoordinatorDeps{
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
		ResponseTimeout: deps.ResponseTimeout,
	})
	m.execution = execution
	input := agent.NewInputCoordinator(agent.InputCoordinatorDeps{
		Sessions:      m.sessions,
		SessionRows:   deps.Store.Sessions(),
		Turns:         m.turns,
		Identity:      identity,
		Hooks:         bridge,
		Output:        output,
		Execution:     execution,
		Confirmations: confirmations,
		Registry:      deps.Registry,
		Preloader:     deps.Preloader,
		ToolState:     deps.ToolState,
		WaitPolicy:    policy,
	})
	fileCommands := agent.NewFileCommandPreparer(agent.FileCommandPreparerDeps{
		Service:     deps.Files,
		Sessions:    m.sessions,
		SessionRows: deps.Store.Sessions(),
		Turns:       m.turns,
		Requests:    m.requests,
		Identity:    identity,
	})
	commands, err := wireCommands(agent.CommandExecutorDeps{
		Router:        m.commands,
		Sessions:      m.sessions,
		Turns:         m.turns,
		Identity:      identity,
		Execution:     execution,
		Output:        output,
		Confirmations: confirmations,
		Input:         input,
	}, commandbuiltin.Deps{
		Doctor:             deps.Doctor,
		Router:             m.commands,
		Sessions:           m.sessions,
		Requests:           m.requests,
		Turns:              m.turns,
		Store:              deps.Store,
		Scope:              identity.Scope,
		Models:             deps.Models,
		Contexts:           m.contexts,
		Compact:            execution,
		Tools:              deps.Registry,
		Skills:             deps.Skills,
		FileRollback:       deps.Files,
		PrepareFileContext: fileCommands.PrepareFileCommand,
		Hooks:              deps.HookControl,
		SessionState:       sessionCommands,
		Logs:               logging.Reader{Dir: deps.LogDir},
		RuntimeStatus:      status.Snapshot,
	})
	if err != nil {
		return m, err
	}
	handler := agent.NewMessageHandler(agent.MessageHandlerDeps{
		Identity:  identity,
		Hooks:     bridge,
		Output:    output,
		Commands:  commands,
		Input:     input,
		Media:     deps.Media,
		Messages:  deps.Store.Messages(),
		MediaRows: deps.Store.Media(),
	})
	completion := agent.NewCompletion(agent.CompletionDeps{
		Commands:  m.commands,
		Sessions:  m.sessions,
		Turns:     m.turns,
		Store:     deps.Store,
		Identity:  identity,
		Registry:  deps.Registry,
		Preloader: deps.Preloader,
		Execution: execution,
	})
	bindings := m.bindings
	if err := bindings.connectAgentNotifications(signals); err != nil {
		return m, err
	}
	if err := bindings.connectSession(m.sessions, deps.Files.Manager, execution); err != nil {
		return m, err
	}
	if err := m.bindings.connectModels(deps.Models); err != nil {
		return m, err
	}
	m.handler, m.completion, m.identity = handler, completion, identity
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
	m.started = true
	m.sessions.StartNaming(m.ctx)
	return m.bridge.StartPlatformEvents(m.ctx)
}
func (m *Module) Handler() *agent.MessageHandler { return m.handler }
func (m *Module) StatusChanged() *signal.Signal[agentevents.StatusChangedEvent, signal.Empty] {
	return m.statusChanged
}
func (m *Module) Completion() *completion.Service        { return m.completion }
func (m *Module) Commands() *command.Router              { return m.commands }
func (m *Module) Sessions() *session.Service             { return m.sessions }
func (m *Module) Execution() *agent.ExecutionCoordinator { return m.execution }
func (m *Module) Identity() agent.BackgroundIdentity     { return m.identity }
func (m *Module) ObserveHookRun() hook.Observer          { return m.bridge.ObserveRun }

// wireExecution installs synchronous participants on this assembly's private Sessions.
func wireExecution(ctx context.Context, deps agent.ExecutionCoordinatorDeps) *agent.ExecutionCoordinator {
	execution := agent.NewExecutionCoordinator(ctx, deps)
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
func wireCommands(deps agent.CommandExecutorDeps, modules commandbuiltin.Deps) (*agent.CommandExecutor, error) {
	executor := agent.NewCommandExecutor(deps)
	if err := commandbuiltin.RegisterDefaultModules(deps.Router, modules); err != nil {
		return nil, err
	}
	return executor, nil
}
func bindProtocolRoutes(registry *routes.Registry, chat *chatroute.Loop, response *responseroute.Loop) {
	registry.Register(routes.Binding{APIType: llm.APITypeChat, Loop: chat, Compactor: chat.Compactor(), Material: chat.Material()})
	registry.Register(routes.Binding{APIType: llm.APITypeResponse, Loop: response, Compactor: response.Compactor(), Material: response.Material()})
}
