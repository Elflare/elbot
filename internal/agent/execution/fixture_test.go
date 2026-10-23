package execution

import (
	"context"
	"elbot/internal/agent/routes"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/fileops"
	"elbot/internal/media"
	"elbot/internal/toolrun"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	chatroute "elbot/internal/agent/chat"
	agentconfirmation "elbot/internal/agent/confirmation"
	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	agenthooks "elbot/internal/agent/hooks"
	agentinput "elbot/internal/agent/input"
	agentoutput "elbot/internal/agent/output"
	responseroute "elbot/internal/agent/responses"
	agentshare "elbot/internal/agent/share"
	agenttools "elbot/internal/agent/tools"
	"elbot/internal/background"
	backgroundmodule "elbot/internal/background/module"
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
	commandDeps   agentinput.CommandExecutorDeps
	messages      storage.MessageRepository
	media         *media.Manager
	commands      agentinput.MessageCommands
	policy        *agentconfirmation.ConfirmationPolicy
	files         *fileops.Service
	toolState     *toolrun.StateService
	confirmations *agentconfirmation.ConfirmationCoordinator
	input         *agentinput.InputCoordinator
	dialogue      *dialogue.Runner
	message       *agentinput.MessageHandler
	background    *backgroundmodule.BackgroundRunner
	execution     *ExecutionCoordinator
	fileCommands  *agentinput.FileCommandPreparer
	identity      *agentshare.IdentityResolver
	hooks         *agenthooks.HookBridge
	output        *fixtureOutputSender
	status        *StatusRecorder
	completion    *completion.Service
	signals       agentevents.Signals
	doneOnce      sync.Once
	done          chan struct{}
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
	a.hooks.BeginClose()
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
	return a.hooks.StartPlatformEvents(ctx)
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
	hookManager := opts.HookManager
	if hookManager == nil {
		hookManager = newTestHookManager()
	}
	if opts.HookRuntime == nil {
		opts.HookRuntime = &fakeHookRouter{}
	}
	signals := agentevents.NewSignals()
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: opts.Platform.Name(), ActorID: "cli:local", ScopeID: "local", Policy: opts.SecurityPolicy})
	hooks := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{
		Manager:    hookManager,
		Router:     opts.HookRuntime,
		Requests:   requests,
		Identity:   identity,
		Media:      opts.Media,
		Failed:     signals.HookFailed,
		Dispatcher: opts.Dispatcher,
	})
	view := dialogue.ExecutionView{Sessions: store.Sessions()}
	status := NewStatusRecorder(StatusRecorderDeps{View: view, Turns: turns, Changed: signals.StatusChanged})
	output := &fixtureOutputSender{OutputSender: agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{Dispatcher: opts.Dispatcher, Hooks: hooks}), dispatcher: opts.Dispatcher}
	sessions.SetForegroundCheck(func(_ context.Context, source *storage.Session) error {
		return view.CheckSelection(source, opts.Models.ResolveMode(storage.SessionModeWork))
	})
	waitPolicy := agentconfirmation.NewConfirmationPolicy(agentconfirmation.ConfirmationPolicyDeps{Identity: identity, IdleExpiration: sessionIdleExpirationConfig(opts.SessionIdleExpiration), UserConfirmationTimeout: 10 * time.Minute})
	confirmations := agentconfirmation.NewConfirmationCoordinator(agentconfirmation.ConfirmationCoordinatorDeps{Sessions: sessions, Requests: requests, Turns: turns, Commands: opts.Commands, Identity: identity, Output: output, Policy: waitPolicy})
	toolRuntime := agenttools.NewToolRuntimeState(agenttools.ToolRuntimeStateDeps{Identity: identity, Provider: opts.ToolProvider, Manager: opts.ToolRunner, Registry: opts.ToolRegistry, FileRollback: opts.FileRollback})
	toolDeps := agenttools.NewToolRunDeps(agenttools.ToolRunDeps{Status: status, Hooks: hooks, Requests: requests, Turns: turns, Identity: identity, State: opts.ToolState, Runtime: toolRuntime, Sessions: sessions, SessionRows: store.Sessions(), ToolCalls: store.ToolCalls(), Confirmations: confirmations, View: view})
	runner := dialogue.NewRunner(dialogue.RunnerDeps{Status: status, Routes: opts.Routes, Dialogues: store.Dialogues(), Sessions: sessions, Turns: turns, Contexts: opts.Contexts, Media: opts.Media, Identity: identity, Hooks: hooks, Output: output, ToolManager: opts.ToolRunner, ToolRegistry: opts.ToolRegistry, ToolState: opts.ToolState, ToolDeps: toolDeps, ToolProvider: toolRuntime.Provider(), DefaultProvider: toolRuntime.DefaultProvider(), MaxRounds: opts.ToolsConfig.MaxRoundsPerTurn, SoulPath: opts.SoulPath, Memory: opts.ResidentMemoryStore, Preloader: opts.ToolPreloader, Signals: signals, View: view})
	chat := chatroute.NewLoop(chatroute.LoopDeps{Status: status, Store: store, Contexts: opts.Contexts, Models: opts.Models, Turns: turns, View: view, Preparer: runner.Preparer, Tools: runner.Preparer.Tools, Messages: runner.Messages, Calls: runner.Calls, System: runner.System})
	response := responseroute.NewLoop(responseroute.LoopDeps{Status: status, Repository: store.Dialogues(), Models: opts.Models, Turns: turns, View: view, Preparer: runner.Preparer, Tools: runner.Preparer.Tools, Messages: runner.Messages, Calls: runner.Calls, System: runner.System})
	bindProtocolRoutes(opts.Routes, chat, response)
	sessions.SetMaterials(opts.Routes)
	if err := opts.Contexts.CheckCompaction(llm.Origin{APIType: llm.APITypeChat}); err != nil {
		return nil, fmt.Errorf("context compaction wiring: %w", err)
	}
	execution := NewExecutionCoordinator(ctx, ExecutionCoordinatorDeps{
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
	input := agentinput.NewInputCoordinator(agentinput.InputCoordinatorDeps{
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
	commandDeps := agentinput.CommandExecutorDeps{
		Router:        opts.Commands,
		Sessions:      sessions,
		Turns:         turns,
		Identity:      identity,
		Execution:     execution,
		Output:        output,
		Confirmations: confirmations,
		Input:         input,
	}
	commands := agentinput.NewCommandExecutor(commandDeps)
	message := agentinput.NewMessageHandler(agentinput.MessageHandlerDeps{
		Identity:  identity,
		Hooks:     hooks,
		Output:    output,
		Commands:  commands,
		Input:     input,
		Media:     opts.Media,
		Messages:  store.Messages(),
		MediaRows: store.Media(),
	})
	background := backgroundmodule.NewBackgroundRunner(backgroundmodule.BackgroundRunnerDeps{
		Sessions:    sessions,
		SessionRows: store.Sessions(),
		Identity:    identity,
		Execution:   execution,
		Preloader:   opts.ToolPreloader,
		ToolState:   opts.ToolState,
		SandboxRoot: filepath.Clean(strings.TrimSpace(opts.SandboxRoot)),
	})
	fileCommands := agentinput.NewFileCommandPreparer(agentinput.FileCommandPreparerDeps{Service: opts.FileRollback, Sessions: sessions, SessionRows: store.Sessions(), Turns: turns, Requests: requests, Identity: identity})
	a := &agentFixture{commandDeps: commandDeps, messages: opts.Store.Messages(), media: opts.Media, commands: commands, policy: waitPolicy, files: opts.FileRollback, toolState: opts.ToolState, confirmations: confirmations,
		message:      message,
		background:   background,
		fileCommands: fileCommands,
		input:        input, dialogue: runner, execution: execution, identity: identity, hooks: hooks, output: output, status: status,
		completion: agentinput.NewCompletion(agentinput.CompletionDeps{
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

type fixtureOutputSender struct {
	*agentoutput.OutputSender
	dispatcher *dispatch.Router
	reasoning  func(context.Context) dialogue.ReasoningStream
}

func (o *fixtureOutputSender) StartReasoning(ctx context.Context) dialogue.ReasoningStream {
	if o.reasoning != nil {
		return o.reasoning(ctx)
	}
	return o.OutputSender.StartReasoning(ctx)
}
func bindProtocolRoutes(registry *routes.Registry, chat *chatroute.Loop, response *responseroute.Loop) {
	registry.Register(routes.Binding{APIType: llm.APITypeChat, Loop: chat, Compactor: chat.Compactor(), Material: chat.Material()})
	registry.Register(routes.Binding{APIType: llm.APITypeResponse, Loop: response, Compactor: response.Compactor(), Material: response.Material()})
}

func setTestConfirmationTimeout(a *agentFixture, timeout time.Duration) {
	*a.policy = *agentconfirmation.NewConfirmationPolicy(agentconfirmation.ConfirmationPolicyDeps{Identity: a.identity, IdleExpiration: a.policy.IdleExpiration(), UserConfirmationTimeout: timeout})
}
