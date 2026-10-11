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
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/llm/responses"
	"elbot/internal/logging"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// Module owns the foreground business line and its independently managed Session, Request and Turn state.
type Module struct {
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
	if deps.Store == nil || deps.Models == nil {
		return m, fmt.Errorf("dialogue store and models are required")
	}
	m.routes = routes.New()
	m.sessions = session.NewServiceWithConfig(deps.Store, session.Config{NamingConfig: session.NamingConfig{TriggerStep: deps.Session.Naming.TriggerStep}, DefaultMode: deps.Session.DefaultMode}, session.NewTitleGenerator(deps.Models))
	m.requests = request.NewManager(0)
	m.turns = turn.NewManager()
	m.contexts = contextmgr.New(contextmgr.Options{Compactors: m.routes, Store: deps.Store, Models: deps.Models, Config: deps.Context, Metadata: deps.Metadata, Providers: deps.Providers})
	m.commands = command.NewRouter(deps.Prefixes)
	sessionCommands := commandbuiltin.NewSessionCommandState(deps.PageSize, deps.RetentionDays)
	signals := agentevents.NewSignals()
	identity, err := agent.NewIdentityResolver(agent.IdentityResolverDeps{PlatformName: deps.PlatformName, ActorID: "cli:local", ScopeID: "local", Policy: deps.Policy})
	if err != nil {
		return m, err
	}
	bridge, err := agent.NewHookBridge(agent.HookBridgeDeps{
		Manager:    deps.HookRunner,
		Router:     deps.HookRouter,
		Requests:   m.requests,
		Identity:   identity,
		Media:      deps.Media,
		Failed:     signals.HookFailed,
		Dispatcher: deps.Dispatcher,
	})
	if err != nil {
		return m, err
	}
	m.bridge = bridge
	status, err := agent.NewStatusRecorder(agent.StatusRecorderDeps{Turns: m.turns, Changed: signals.StatusChanged})
	if err != nil {
		return m, err
	}
	output, err := agent.NewOutputSender(agent.OutputSenderDeps{Dispatcher: deps.Dispatcher, Notifications: deps.Notifications, Hooks: bridge, Identity: identity})
	if err != nil {
		return m, err
	}
	policy, err := agent.NewConfirmationPolicy(agent.ConfirmationPolicyDeps{Identity: identity, IdleExpiration: sessionIdleExpirationConfig(deps.Session.IdleExpiration), UserConfirmationTimeout: 10 * time.Minute})
	if err != nil {
		return m, err
	}
	confirmations, err := agent.NewConfirmationCoordinator(agent.ConfirmationCoordinatorDeps{
		Sessions: m.sessions,
		Requests: m.requests,
		Turns:    m.turns,
		Commands: m.commands,
		Identity: identity,
		Output:   output,
		Policy:   policy,
	})
	if err != nil {
		return m, err
	}
	toolRuntime, err := agent.NewToolRuntimeState(agent.ToolRuntimeStateDeps{Provider: nil, Manager: deps.ToolManager, Registry: deps.Registry, FileRollback: deps.Files, Identity: identity})
	if err != nil {
		return m, err
	}
	view := dialogue.ExecutionView{Sessions: deps.Store.Sessions(), Providers: m.routes}
	toolDeps := agent.NewToolRunDeps(agent.ToolRunDeps{
		Hooks:         bridge,
		Requests:      m.requests,
		Turns:         m.turns,
		Identity:      identity,
		Media:         deps.Media,
		State:         deps.ToolState,
		Runtime:       toolRuntime,
		Sessions:      m.sessions,
		SessionRows:   deps.Store.Sessions(),
		ToolCalls:     deps.Store.ToolCalls(),
		Confirmations: confirmations,
		View:          view,
	})
	runner, err := dialogue.NewRunner(dialogue.RunnerDeps{
		Routes:          m.routes,
		Dialogues:       deps.Store.Dialogues(),
		MessageRows:     deps.Store.Messages(),
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
	if err != nil {
		return m, err
	}
	chat, err := chatroute.NewLoop(chatroute.LoopDeps{
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
	if err != nil {
		return m, err
	}
	response, err := responseroute.NewLoop(responseroute.LoopDeps{
		Repository: deps.Store.Dialogues(),
		Media:      deps.Media,
		Models:     deps.Models,
		Turns:      m.turns,
		View:       view,
		Preparer:   runner.Preparer,
		Tools:      runner.Preparer.Tools,
		Messages:   runner.Messages,
		Calls:      runner.Calls,
		System:     runner.System,
	})
	if err != nil {
		return m, err
	}
	if err := bindProviderRoutes(m.routes, deps.Models, chat, response, chat.Compactor(), response.Compactor(), chat.Material(), response.Material()); err != nil {
		return m, err
	}
	m.sessions.SetMaterials(m.routes)
	if err := m.contexts.CheckCompaction(llm.Origin{APIType: llm.APITypeChat}); err != nil {
		return m, fmt.Errorf("context compaction wiring: %w", err)
	}
	m.sessions.SetForegroundCheck(func(_ context.Context, source *storage.Session) error {
		return view.CheckSelection(source, deps.Models.ResolveMode(storage.SessionModeWork))
	})
	execution, err := wireExecution(ctx, agent.ExecutionCoordinatorDeps{
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
	if err != nil {
		return m, err
	}
	m.execution = execution
	input, err := agent.NewInputCoordinator(agent.InputCoordinatorDeps{
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
	if err != nil {
		return m, err
	}
	fileCommands, err := agent.NewFileCommandPreparer(agent.FileCommandPreparerDeps{
		Service:     deps.Files,
		Sessions:    m.sessions,
		SessionRows: deps.Store.Sessions(),
		Turns:       m.turns,
		Requests:    m.requests,
		Identity:    identity,
	})
	if err != nil {
		return m, err
	}
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
		Providers:          m.routes,
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
	handler, err := agent.NewMessageHandler(agent.MessageHandlerDeps{
		Identity:  identity,
		Hooks:     bridge,
		Output:    output,
		Commands:  commands,
		Input:     input,
		Media:     deps.Media,
		Messages:  deps.Store.Messages(),
		MediaRows: deps.Store.Media(),
	})
	if err != nil {
		return m, err
	}
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
	if err := bindings.connectAgentNotifications(signals, deps.Notifications, output); err != nil {
		return m, err
	}
	if err := bindings.connectStatus(signals, m.sessions, deps.Dispatcher); err != nil {
		return m, err
	}
	if err := bindings.connectSession(m.sessions, deps.Files.Manager); err != nil {
		return m, err
	}
	if err := m.bindings.connectModels(deps.Models, deps.Notifications); err != nil {
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
func (m *Module) Handler() *agent.MessageHandler         { return m.handler }
func (m *Module) Completion() *completion.Service        { return m.completion }
func (m *Module) Commands() *command.Router              { return m.commands }
func (m *Module) Sessions() *session.Service             { return m.sessions }
func (m *Module) Execution() *agent.ExecutionCoordinator { return m.execution }
func (m *Module) Identity() agent.BackgroundIdentity     { return m.identity }
func (m *Module) ObserveHookRun() hook.Observer          { return m.bridge.ObserveRun }

// wireExecution installs synchronous participants on this assembly's private Sessions.
func wireExecution(ctx context.Context, deps agent.ExecutionCoordinatorDeps) (*agent.ExecutionCoordinator, error) {
	execution, err := agent.NewExecutionCoordinator(ctx, deps)
	if err != nil {
		return nil, err
	}
	deps.Sessions.SetForegroundActivation(execution.AdoptForeground)
	deps.Sessions.SetActivitySource(func() []string {
		var ids []string
		for _, active := range deps.Turns.SnapshotAll() {
			if active.Phase != turn.PhaseIdle {
				ids = append(ids, active.SessionID)
			}
		}
		return ids
	})
	return execution, nil
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
	executor, err := agent.NewCommandExecutor(deps)
	if err != nil {
		return nil, err
	}
	if err := commandbuiltin.RegisterDefaultModules(deps.Router, modules); err != nil {
		return nil, err
	}
	return executor, nil
}
func bindProviderRoutes(registry *routes.Registry, models *modelmgr.Service, chat, response dialogue.Loop, compactor, nativeCompactor contextmgr.Compactor, display, material session.MaterialPreparer) error {
	if err := registry.RegisterCompactor(llm.APITypeChat, compactor); err != nil {
		return err
	}
	if err := registry.RegisterMaterial(llm.APITypeChat, display); err != nil {
		return err
	}
	if nativeCompactor != nil {
		if err := registry.RegisterCompactor(llm.APITypeResponse, nativeCompactor); err != nil {
			return err
		}
	}
	if material != nil {
		if err := registry.RegisterMaterial(llm.APITypeResponse, material); err != nil {
			return err
		}
	}
	for _, origin := range models.ProviderOrigins() {
		client := models.ClientForProvider(origin.Provider)
		registered, err := registry.CheckProviderBinding(origin, client)
		if err != nil {
			return err
		}
		if registered {
			continue
		}
		binding := routes.Binding{Origin: origin, Client: client}
		switch origin.APIType {
		case llm.APITypeChat:
			if _, ok := client.(chatcompletions.Streamer); !ok {
				return fmt.Errorf("provider %q api_mode %q requires the Chat streaming capability", origin.Provider, origin.APIType)
			}
			binding.Loop, binding.Compactor = chat, compactor
			binding.Material = display
		case llm.APITypeResponse:
			if _, ok := client.(responses.Streamer); !ok {
				return fmt.Errorf("provider %q api_mode %q requires the Responses streaming capability", origin.Provider, origin.APIType)
			}
			binding.Loop, binding.Compactor, binding.Material = response, nativeCompactor, material
		default:
			return fmt.Errorf("provider %q has unsupported api_mode %q", origin.Provider, origin.APIType)
		}
		if err := registry.Register(binding); err != nil {
			return err
		}
	}
	return registry.Seal()
}
