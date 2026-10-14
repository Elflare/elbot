package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	chatroute "elbot/internal/agent/chat"
	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	responseroute "elbot/internal/agent/responses"
	"elbot/internal/agent/routes"
	"elbot/internal/background"
	"elbot/internal/completion"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
)

// agentFixture exposes the application's message, execution and observation capabilities.
// Business state and dependencies belong to the components behind these entries.
// agentFixture is a test fixture assembling independently constructed components.
type agentFixture struct {
	input        *InputCoordinator
	dialogue     *dialogue.Runner
	message      *MessageHandler
	background   *BackgroundRunner
	execution    *ExecutionCoordinator
	fileCommands *FileCommandPreparer
	identity     *IdentityResolver
	hooks        *HookBridge
	output       *OutputSender
	status       *StatusRecorder
	completion   *completion.Service
	signals      agentevents.Signals
	doneOnce     sync.Once
	done         chan struct{}
}

func (a *agentFixture) Signals() agentevents.Signals { return a.signals }

func (a *agentFixture) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	return a.background.RunBackground(ctx, req)
}

func (a *agentFixture) CompletionService() *completion.Service {
	return a.completion
}

func (a *agentFixture) AdoptForeground(ctx context.Context, row *storage.Session, binding *session.Binding) {
	a.execution.AdoptForeground(ctx, row, binding)
}

func (a *agentFixture) CompactCurrent(ctx context.Context, triggerReason string) (string, error) {
	return a.execution.CompactCurrent(ctx, triggerReason)
}

func (a *agentFixture) BeginClose() {
	a.hooks.beginClosePlatformEvents()
	a.execution.appendWaits.beginClose()
}

func (a *agentFixture) Close(ctx context.Context) error {
	a.BeginClose()
	select {
	case <-a.Done():
		return nil
	default:
	}
	select {
	case <-a.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *agentFixture) Done() <-chan struct{} {
	a.doneOnce.Do(func() {
		a.done = make(chan struct{})
		go func() { <-a.execution.Done(); a.hooks.BeginClose(); <-a.hooks.Done(); close(a.done) }()
	})
	return a.done
}

func (a *agentFixture) PrepareFileCommand(ctx context.Context, idleOnly bool) (context.Context, error) {
	return a.fileCommands.PrepareFileCommand(ctx, idleOnly)
}

func (a *agentFixture) ObserveHookRun(ctx context.Context, event hook.Event, info hook.ObserverInfo) (context.Context, func()) {
	if a == nil || a.hooks == nil {
		return ctx, func() {}
	}
	return a.hooks.ObserveRun(ctx, event, info)
}

func (a *agentFixture) Scope(ctx context.Context) session.Scope { return a.identity.Scope(ctx) }

func (a *agentFixture) HandleMessage(ctx context.Context, text string) error {
	ctx = context.WithValue(ctx, fixtureNotificationRouteKey{}, a.output.dispatcher)
	return a.message.HandleMessage(ctx, text)
}

func (a *agentFixture) NotificationSender() interface {
	SendAssistant(context.Context, string) (delivery.Receipt, globalevents.LogResult, error)
} {
	return a.output
}

func (a *agentFixture) StartPlatformEvents(ctx context.Context) error {
	return a.hooks.startPlatformEvents(ctx)
}

func (a *agentFixture) RuntimeStatus(sessionID string) runtimestatus.Snapshot {
	return a.status.Snapshot(sessionID)
}

func (a *agentFixture) HookWakeup(ctx context.Context, event hook.Event) bool {
	return a.message.HookWakeup(ctx, event)
}

func newTestAssembly(ctx context.Context, opts testAssembly) (*agentFixture, error) {
	store := opts.Store
	requests, turns, sessions := opts.Requests, opts.Turns, opts.Sessions
	hookManager := hookRunner(opts.HookManager)
	if hookManager == nil {
		hookManager = hook.NoopManager{}
	}
	signals := agentevents.NewSignals()
	identity, err := NewIdentityResolver(IdentityResolverDeps{PlatformName: opts.Platform.Name(), ActorID: "cli:local", ScopeID: "local", Policy: opts.SecurityPolicy})
	if err != nil {
		return nil, err
	}
	hooks, err := NewHookBridge(HookBridgeDeps{
		Manager:    hookManager,
		Router:     opts.HookRuntime,
		Requests:   requests,
		Identity:   identity,
		Media:      opts.Media,
		Failed:     signals.HookFailed,
		Dispatcher: opts.Dispatcher,
	})
	if err != nil {
		return nil, err
	}
	view := dialogue.ExecutionView{Sessions: store.Sessions()}
	status, err := NewStatusRecorder(StatusRecorderDeps{View: view, Turns: turns, Changed: signals.StatusChanged})
	if err != nil {
		return nil, err
	}
	output, err := NewOutputSender(OutputSenderDeps{Dispatcher: opts.Dispatcher, Hooks: hooks})
	if err != nil {
		return nil, err
	}
	sessions.SetForegroundCheck(func(_ context.Context, source *storage.Session) error {
		return view.CheckSelection(source, opts.Models.ResolveMode(storage.SessionModeWork))
	})
	waitPolicy, err := NewConfirmationPolicy(ConfirmationPolicyDeps{Identity: identity, IdleExpiration: sessionIdleExpirationConfig(opts.SessionIdleExpiration), UserConfirmationTimeout: defaultUserConfirmationTimeout})
	if err != nil {
		return nil, err
	}
	confirmations, err := NewConfirmationCoordinator(ConfirmationCoordinatorDeps{Sessions: sessions, Requests: requests, Turns: turns, Commands: opts.Commands, Identity: identity, Output: output, Policy: waitPolicy})
	if err != nil {
		return nil, err
	}
	toolRuntime := &ToolRuntimeState{
		manager: opts.ToolRunner, registry: opts.ToolRegistry,
		fileRollback: opts.FileRollback, provider: dialogue.NoopToolSchemaProvider{}, defaultProvider: true,
	}
	if opts.ToolProvider != nil {
		toolRuntime.provider, toolRuntime.defaultProvider = opts.ToolProvider, false
	} else {
		toolRuntime.provider = toolRunPromptProvider{tools: toolRuntime.manager, identity: identity}
	}
	toolDeps := &toolRunDeps{status: status,
		hooks: hooks, requests: requests, turns: turns, identity: identity,
		state: opts.ToolState, runtime: toolRuntime, sessions: sessions, sessionRows: store.Sessions(), toolCalls: store.ToolCalls(),
		confirmations: confirmations, view: view,
	}
	runner, err := dialogue.NewRunner(dialogue.RunnerDeps{Status: status, Routes: opts.Routes, Dialogues: store.Dialogues(), MessageRows: store.Messages(), Sessions: sessions, Turns: turns, Contexts: opts.Contexts, Media: opts.Media, Identity: identity, Hooks: hooks, Output: output, ToolManager: opts.ToolRunner, ToolRegistry: opts.ToolRegistry, ToolState: opts.ToolState, ToolDeps: toolDeps, ToolProvider: toolRuntime.provider, DefaultProvider: toolRuntime.defaultProvider, MaxRounds: opts.ToolsConfig.MaxRoundsPerTurn, SoulPath: opts.SoulPath, Memory: opts.ResidentMemoryStore, Preloader: opts.ToolPreloader, Signals: signals, View: view})
	if err != nil {
		return nil, err
	}
	chat, err := chatroute.NewLoop(chatroute.LoopDeps{Status: status, Store: store, Contexts: opts.Contexts, Models: opts.Models, Turns: turns, View: view, Preparer: runner.Preparer, Tools: runner.Preparer.Tools, Messages: runner.Messages, Calls: runner.Calls, System: runner.System})
	if err != nil {
		return nil, err
	}
	response, err := responseroute.NewLoop(responseroute.LoopDeps{Status: status, Repository: store.Dialogues(), Models: opts.Models, Turns: turns, View: view, Preparer: runner.Preparer, Tools: runner.Preparer.Tools, Messages: runner.Messages, Calls: runner.Calls, System: runner.System})
	if err != nil {
		return nil, err
	}
	if err := bindProtocolRoutes(opts.Routes, chat, response); err != nil {
		return nil, err
	}
	sessions.SetMaterials(opts.Routes)
	if err := opts.Contexts.CheckCompaction(llm.Origin{APIType: llm.APITypeChat}); err != nil {
		return nil, fmt.Errorf("context compaction wiring: %w", err)
	}
	execution, err := NewExecutionCoordinator(ctx, ExecutionCoordinatorDeps{
		Sessions:        sessions,
		SessionRows:     store.Sessions(),
		Turns:           turns,
		Requests:        requests,
		Contexts:        opts.Contexts,
		Models:          opts.Models,
		Dialogue:        runner,
		Identity:        identity,
		View:            view,
		Output:          output,
		Status:          status,
		WaitPolicy:      waitPolicy,
		ResponseTimeout: responseTimeout(opts.LLMRequestConfig),
	})
	if err != nil {
		return nil, err
	}
	input, err := NewInputCoordinator(InputCoordinatorDeps{
		Sessions:      sessions,
		SessionRows:   store.Sessions(),
		Turns:         turns,
		Identity:      identity,
		Hooks:         hooks,
		Output:        output,
		Execution:     execution,
		Confirmations: confirmations,
		Registry:      opts.ToolRegistry,
		Preloader:     opts.ToolPreloader,
		ToolState:     opts.ToolState,
		WaitPolicy:    waitPolicy,
	})
	if err != nil {
		return nil, err
	}
	commands, err := NewCommandExecutor(CommandExecutorDeps{
		Router:        opts.Commands,
		Sessions:      sessions,
		Turns:         turns,
		Identity:      identity,
		Execution:     execution,
		Output:        output,
		Confirmations: confirmations,
		Input:         input,
	})
	if err != nil {
		return nil, err
	}
	message, err := NewMessageHandler(MessageHandlerDeps{
		Identity:  identity,
		Hooks:     hooks,
		Output:    output,
		Commands:  commands,
		Input:     input,
		Media:     opts.Media,
		Messages:  store.Messages(),
		MediaRows: store.Media(),
	})
	if err != nil {
		return nil, err
	}
	background, err := NewBackgroundRunner(BackgroundRunnerDeps{
		Sessions:    sessions,
		SessionRows: store.Sessions(),
		Identity:    identity,
		Execution:   execution,
		Preloader:   opts.ToolPreloader,
		ToolState:   opts.ToolState,
		SandboxRoot: filepath.Clean(strings.TrimSpace(opts.SandboxRoot)),
	})
	if err != nil {
		return nil, err
	}
	fileCommands, err := NewFileCommandPreparer(FileCommandPreparerDeps{Service: opts.FileRollback, Sessions: sessions, SessionRows: store.Sessions(), Turns: turns, Requests: requests, Identity: identity})
	if err != nil {
		return nil, err
	}
	a := &agentFixture{
		message:      message,
		background:   background,
		fileCommands: fileCommands,
		input:        input, dialogue: runner, execution: execution, identity: identity, hooks: hooks, output: output, status: status,
		completion: NewCompletion(CompletionDeps{
			Commands:  opts.Commands,
			Sessions:  sessions,
			Turns:     turns,
			Store:     store,
			Identity:  identity,
			Registry:  opts.ToolRegistry,
			Preloader: opts.ToolPreloader,
			Execution: execution,
		}), signals: signals,
	}
	a.Done() // Join owned workers on application cancellation, even without Close.
	return a, nil
}
func bindProtocolRoutes(registry *routes.Registry, chat *chatroute.Loop, response *responseroute.Loop) error {
	if err := registry.Register(routes.Binding{APIType: llm.APITypeChat, Loop: chat, Compactor: chat.Compactor(), Material: chat.Material()}); err != nil {
		return err
	}
	if err := registry.Register(routes.Binding{APIType: llm.APITypeResponse, Loop: response, Compactor: response.Compactor(), Material: response.Material()}); err != nil {
		return err
	}
	return registry.Seal()
}
