package agent

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/signal"
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
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/control"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/request"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	if deps.Control == nil {
		return nil, fmt.Errorf("agent: Control is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	m := &Supervisor{ctx: ctx, cancel: cancel, statusChanged: signal.New[agentexecution.StatusChangedEvent, signal.Empty]("agent.status_changed"), done: make(chan struct{})}
	m.routes = routes.New()
	m.requests = request.NewManager(deps.Store.NextIDSequence)
	m.turns = turn.NewSupervisor(deps.Store.NextIDSequence)
	m.contexts = contextmgr.New(contextmgr.Options{Compactors: m.routes, Store: deps.Store, Models: deps.Models, Config: cfg.Context})
	m.commands = command.NewRouter(cfg.Commands.Prefixes)
	prefix := cfg.Commands.Prefixes[0]
	commandText := func(name string, args ...string) string { return command.Format(prefix, name, args...) }
	view := dialogue.ExecutionView{Sessions: deps.Store.Sessions()}
	m.restoreMessageContext = deps.Platform.RestoreMessageContext
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: config.PlatformCLI, Permission: deps.Permission})
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{
		Identity: identity,
	})
	m.bridge = bridge
	status := agentexecution.NewStatusRecorder(agentexecution.StatusRecorderDeps{View: view, Turns: m.turns, Changed: m.statusChanged})
	// 构造不执行释放回调；全部消费者赋值后才启动 Session。
	var confirmations *agentconfirmation.ConfirmationCoordinator
	var err error
	m.sessions, err = session.NewSupervisor(ctx, cfg, session.SupervisorDeps{
		AdoptForeground: func(ctx context.Context, row *storage.Session, b *session.Binding) {
			m.execution.AdoptForeground(ctx, row, b)
		},
		SessionIDCode: deps.Platform.SessionIDCode,
		Store:         deps.Store, Models: deps.Models, Materials: m.routes,
		ActiveSessionIDs: func() []string { return m.execution.ActiveSessionIDs() },
		CheckForeground: func(_ context.Context, source *storage.Session) (operationresult.Result[struct{}], error) {
			return view.CheckSelection(source, deps.Models.ResolveMode(storage.SessionModeWork))
		},
		IsSuperadmin: func(scope session.Scope) bool {
			return deps.Permission.IsSuperadmin(scope.Owner.Platform, scope.Owner.ID)
		},
		ReleaseSession: func(id string) {
			status.ForgetSession(id)
			confirmations.ForgetSession(id)
		},
		ReleaseBinding: func(binding *session.Binding) { deps.Tools.Forget(binding) },
	})
	if err != nil {
		return m, err
	}
	output := agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{Dispatcher: deps.Platform, Hooks: bridge})
	policy := agentconfirmation.NewConfirmationPolicy(agentconfirmation.ConfirmationPolicyDeps{Identity: identity, IdleExpiration: cfg.Session.IdleExpiration, UserConfirmationTimeout: 10 * time.Minute})
	confirmations = agentconfirmation.NewConfirmationCoordinator(agentconfirmation.ConfirmationCoordinatorDeps{
		CommandText: commandText,
		Sessions:    m.sessions,
		Requests:    m.requests,
		Turns:       m.turns,
		Identity:    identity,
		Output:      output,
		Policy:      policy,
	})
	toolRuntime := agenttools.NewToolRuntimeState(agenttools.ToolRuntimeStateDeps{Manager: deps.Tools, FileRollback: deps.Tools, Identity: identity})
	toolDeps := agenttools.NewToolRunDeps(agenttools.ToolRunDeps{
		NewWorkspaceStore: func(id string) workspace.WorkspaceStore {
			return m.sessions.WorkspaceStore(id)
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
		NewMessageID:         storage.IDSequence(deps.Store.NextIDSequence).MessageID,
		Status:               status,
		Routes:               m.routes,
		Dialogues:            deps.Store.Dialogues(),
		Sessions:             m.sessions,
		Turns:                m.turns,
		Contexts:             m.contexts,
		Media:                deps.Media,
		PublishUsageFinished: publishMediaUsageFinished(deps.Media),
		Identity:             identity,
		Hooks:                bridge,
		Output:               output,
		ToolManager:          deps.Tools,
		ToolDeps:             toolDeps,
		ToolProvider:         toolRuntime.Provider(),
		MaxRounds:            cfg.Tools.MaxRoundsPerTurn,
		SoulPath:             cfg.Soul.Path,
		Memory:               deps.Tools,
		Preloader:            deps.Tools,
		View:                 view,
	})
	chat := chatroute.NewLoop(chatroute.LoopDeps{
		NewMessageID: storage.IDSequence(deps.Store.NextIDSequence).MessageID,
		Identity:     identity,
		SelectionForTurn: func(ctx context.Context, row *storage.Session) modelmgr.Selection {
			return modelmgr.SelectionForTurn(ctx, deps.Models, row)
		},
		NewToolTranscriptCommitter: func() agentshare.ToolTranscriptCommitter {
			return &dialogue.ToolTranscriptCommitter{Messages: runner.Messages}
		},
		Status:   status,
		Turns:    m.turns,
		View:     view,
		Preparer: runner.Preparer,
		Tools:    runner.Preparer.Tools,
		Messages: runner.Messages,
		Caller: &chatroute.Caller{
			Calls: runner.Calls, Messages: runner.Messages, Preparer: runner.Preparer,
		},
		PromptBuilder: chatroute.PromptBuilder{System: runner.System},
	}, &chatroute.Compactor{
		ToolCalls: deps.Store.ToolCalls(),
		Models:    deps.Models,
		Loader:    contextmgr.Loader{Store: deps.Store},
	})
	response := responseroute.NewLoop(responseroute.LoopDeps{
		NewMessageID: storage.IDSequence(deps.Store.NextIDSequence).MessageID,
		Identity:     identity,
		Gate:         runner.Messages.Gate,
		SessionRows:  deps.Store.Sessions(),
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
	execution := agentexecution.NewExecutionCoordinator(ctx, agentexecution.ExecutionCoordinatorDeps{
		AppendConfirmation: delivery.Confirmation{Actions: agentconfirmation.AppendConfirmationActions(commandText)},
		AppendWait:         policy,
		Sessions:           m.sessions,
		SessionRows:        deps.Store.Sessions(),
		Turns:              m.turns,
		Requests:           m.requests,
		Contexts:           m.contexts,
		SelectionForTurn: func(ctx context.Context, row *storage.Session) modelmgr.Selection {
			return modelmgr.SelectionForTurn(ctx, deps.Models, row)
		},
		Dialogue:        runner,
		Identity:        identity,
		View:            view,
		Output:          output,
		Status:          status,
		ResponseTimeout: time.Duration(cfg.LLMRequest.ResponseTimeoutSeconds) * time.Second,
	})
	m.execution = execution
	fileCommands := agentinput.NewFileCommandPreparer(agentinput.FileCommandPreparerDeps{
		NewWorkspaceStore: func(id string) workspace.WorkspaceStore {
			return m.sessions.WorkspaceStore(id)
		},
		Service:  deps.Tools,
		Sessions: m.sessions,
		Turns:    m.turns,
		Requests: m.requests,
		Identity: identity,
	})
	controlAdapter := controlAdapter{supervisor: m, store: deps.Store, models: deps.Models, scope: identity.Scope, status: status}
	workspaces := workspace.NewService(func(ctx context.Context) (operationresult.Result[string], error) {
		current, err := m.sessions.Current(ctx, identity.Scope(ctx))
		if err != nil {
			return operationresult.Result[string]{}, err
		}
		if current.Status != operationresult.Success {
			return operationresult.Result[string]{Status: current.Status, Message: current.Message}, nil
		}
		return operationresult.Result[string]{Status: operationresult.Success, Data: current.Data.ID}, nil
	}, func(id string) workspace.SessionStateStore {
		return m.sessions.WorkspaceStore(id)
	})
	if err := deps.Control.ConnectAgent(control.AgentDeps{Sessions: m.sessions, Workspaces: workspaces, History: controlAdapter, Requests: m.requests, Execution: controlAdapter, Confirmations: confirmations, Scope: identity.Scope, Status: controlAdapter.Status, Commands: controlAdapter.Commands, Tools: deps.Tools, Skills: deps.Tools, FileRollback: fileRollbackCapabilities{tools: deps.Tools}, PrepareFileContext: fileCommands.PrepareFileCommand}); err != nil {
		return m, err
	}
	m.background = agentinput.NewBackgroundHandler(agentinput.BackgroundHandlerDeps{
		Sessions: m.sessions, Identity: identity, Execution: execution, Preloader: deps.Tools, ToolState: deps.Tools,
		EnsureWorkspaceDir: func(ctx context.Context, id, dir string) error {
			return m.sessions.WorkspaceStore(id).EnsureWorkspaceDir(ctx, dir)
		},
	})
	input := agentinput.NewInputCoordinator(agentinput.InputCoordinatorDeps{
		CommandText: commandText,
		NormalizeInput: func(ctx context.Context, sessionID string, segments []llm.MessageSegment) (operationresult.Result[[]llm.MessageSegment], error) {
			ctx = workspace.WithWorkspaceStore(ctx, m.sessions.WorkspaceStore(sessionID))
			return deps.Platform.NormalizeInput(ctx, segments)
		},
		NewWorkspaceStore: func(id string) workspace.WorkspaceStore {
			return m.sessions.WorkspaceStore(id)
		},
		Sessions:      m.sessions,
		Turns:         m.turns,
		Identity:      identity,
		Hooks:         bridge,
		Output:        output,
		Execution:     execution,
		Confirmations: deps.Control,
		Preloader:     deps.Tools,
		ToolState:     deps.Tools,
	})
	m.input = input
	commands, err := wireCommands(m.commands, agentinput.CommandExecutorDeps{Router: m.commands, Sessions: m.sessions, Identity: identity, Output: deps.Platform, Confirmations: deps.Control, Input: input}, command.BuiltinDeps{Control: deps.Control, SessionListPageSize: cfg.View.SessionListPageSize, SessionRetentionDays: cfg.Maintenance.SessionCleanup.RetentionDays})
	if err != nil {
		return m, err
	}
	handler := agentinput.NewMessageHandler(agentinput.MessageHandlerDeps{
		History:   deps.ChatHistory,
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
		completion.WorkspaceFilesSource{Root: input.CompletionWorkspaceRoot},
		completion.RiskConfirmationSource{Router: m.commands, Control: deps.Control, CommandNames: agentconfirmation.RiskConfirmationCommandNames()},
		completion.ForkMessageSource{Router: m.commands, Control: deps.Control, PageSize: cfg.View.SessionListPageSize},
		completion.ToolDirectiveSource{Context: input.ToolCompletionContext, Tools: deps.Tools},
		completion.RouterSource{Router: m.commands, Actor: identity.Actor},
	)
	m.handler = handler
	return m, nil
}

// wireCommands constructs the command consumer and registers its modules.
func wireCommands(router *command.Router, deps agentinput.CommandExecutorDeps, modules command.BuiltinDeps) (*agentinput.CommandExecutor, error) {
	executor := agentinput.NewCommandExecutor(deps)
	if err := command.RegisterBuiltins(router, modules); err != nil {
		return nil, err
	}
	return executor, nil
}

// fileRollbackCapabilities adapts the command's List name to Tool's rollback operation.
type fileRollbackCapabilities struct {
	tools interface {
		ListRollbacks(context.Context) operationresult.Result[[]fileops.RollbackInfo]
		RollbackByID(context.Context, uint64) (operationresult.Result[fileops.RollbackResult], error)
	}
}

func (c fileRollbackCapabilities) List(ctx context.Context) operationresult.Result[[]fileops.RollbackInfo] {
	return c.tools.ListRollbacks(ctx)
}

func (c fileRollbackCapabilities) RollbackByID(ctx context.Context, id uint64) (operationresult.Result[fileops.RollbackResult], error) {
	return c.tools.RollbackByID(ctx, id)
}

func bindProtocolRoutes(registry *routes.Registry, chat *chatroute.Loop, response *responseroute.Loop) {
	registry.Register(routes.Binding{APIType: llm.APITypeChat, Loop: chat, Compactor: chat.Compactor(), Material: chat.Material()})
	registry.Register(routes.Binding{APIType: llm.APITypeResponse, Loop: response, Compactor: response.Compactor(), Material: response.Material()})
}
