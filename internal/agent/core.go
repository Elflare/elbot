package agent

import (
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/hook"
)

// Agent exposes the application's message, execution and observation capabilities.
// Business state and dependencies belong to the components behind these entries.
type Agent struct {
	message      *messageHandler
	background   *backgroundRunner
	execution    *executionCoordinator
	fileCommands *fileCommandPreparer
	identity     *identityResolver
	hooks        *hookBridge
	output       *outputSender
	status       *statusRecorder
	completion   *completion.Service
	signals      Signals
}

func responseTimeout(cfg config.LLMRequestConfig) time.Duration {
	if cfg.ResponseTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.ResponseTimeoutSeconds) * time.Second
}

func NewWithOptions(opts Options) (*Agent, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	store := opts.Store
	requests, turns, sessions := opts.Requests, opts.Turns, opts.Sessions
	var logger, auditLogger *slog.Logger
	if opts.Logs != nil {
		logger, auditLogger = opts.Logs.Runtime(), opts.Logs.Audit()
	}
	hookManager := hookRunner(opts.HookManager)
	if hookManager == nil {
		hookManager = hook.NoopManager{}
	}
	signals := newSignals()
	identity := &identityResolver{platformName: opts.Platform.Name(), actorID: "cli:local", scopeID: "local", policy: opts.SecurityPolicy}
	hooks := &hookBridge{
		manager: hookManager, router: opts.HookRuntime, requests: requests,
		identity: identity, media: opts.Media, failed: signals.HookFailed, dispatcher: opts.Dispatcher, logger: logger,
	}
	status := &statusRecorder{turns: turns, changed: signals.StatusChanged}
	output := &outputSender{dispatcher: opts.Dispatcher, notifications: opts.Notifications, hooks: hooks, identity: identity, logger: logger}
	view := executionView{sessions: store.Sessions()}
	replies := &replyCommitter{messages: store.Messages(), output: output, delivered: signals.ReplyDelivered, committed: signals.ReplyCommitted}
	waitPolicy := &confirmationPolicy{identity: identity, idleExpiration: sessionIdleExpirationConfig(opts.SessionIdleExpiration), userConfirmationTimeout: defaultUserConfirmationTimeout}
	confirmations := &confirmationCoordinator{
		sessions: sessions, requests: requests, turns: turns, commands: opts.Commands,
		identity: identity, output: output, policy: waitPolicy, changed: signals.ConfirmationChanged,
		autoConfirmSession: map[string]bool{}, autoConfirmTools: map[string]map[string]bool{},
	}
	toolRuntime := &toolRuntimeState{
		manager: opts.ToolRunner, registry: opts.ToolRegistry,
		fileRollback: opts.FileRollback, config: opts.ToolsConfig, provider: noopToolSchemaProvider{}, defaultProvider: true,
	}
	if opts.ToolRegistry != nil {
		toolRuntime.provider = toolRunPromptProvider{tools: toolRuntime.manager, identity: identity}
	} else if opts.ToolProvider != nil {
		toolRuntime.provider, toolRuntime.defaultProvider = opts.ToolProvider, false
	}
	toolDeps := &toolRunDeps{
		hooks: hooks, requests: requests, turns: turns, identity: identity, media: opts.Media,
		state: opts.ToolState, runtime: toolRuntime, sessions: sessions, store: store,
		confirmations: confirmations, view: view, completed: signals.ToolCallCompleted, denied: signals.ToolDenied,
	}
	caller := &modelCaller{
		messages: store.Messages(), media: opts.Media, hooks: hooks, identity: identity,
		toolState: opts.ToolState, toolRuntime: toolRuntime, completed: signals.ModelCallCompleted, vision: signals.VisionFallbackUsed, persistenceFailed: signals.PersistenceFailed,
	}
	chat := &chatRunner{
		messages: store.Messages(), media: opts.Media, contexts: opts.Contexts, models: opts.Models, turns: turns, identity: identity,
		hooks: hooks, view: view, toolRuntime: toolRuntime, toolState: opts.ToolState,
		toolDeps: toolDeps, caller: caller, replies: replies, inputReceived: signals.UserInputReceived, persistenceFailed: signals.PersistenceFailed,
		promptBuilder: buildPrompt(opts.SoulPath, opts.ResidentMemoryStore, toolRuntime.provider, opts.ToolPreloader), logger: logger,
	}
	execution := &executionCoordinator{
		sessions: sessions, sessionRows: store.Sessions(), turns: turns, requests: requests, contexts: opts.Contexts,
		models: opts.Models, chat: chat, identity: identity, view: view, output: output, status: status,
		waitPolicy: waitPolicy, responseTimeout: responseTimeout(opts.LLMRequestConfig), persistenceFailed: signals.PersistenceFailed, timedOut: signals.TurnTimedOut, logger: logger,
		appendWaits: newAppendWaitLifecycle(opts.RuntimeContext),
	}
	input := &inputCoordinator{
		sessions: sessions, sessionRows: store.Sessions(), turns: turns, identity: identity,
		hooks: hooks, output: output, execution: execution, confirmations: confirmations,
		registry: opts.ToolRegistry, preloader: opts.ToolPreloader, toolState: opts.ToolState, waitPolicy: waitPolicy, auditLogger: auditLogger,
	}
	commands := &commandExecutor{
		router: opts.Commands, sessions: sessions, turns: turns, identity: identity,
		execution: execution, output: output, confirmations: confirmations, input: input, auditLogger: auditLogger,
	}
	return &Agent{
		message: &messageHandler{
			identity: identity, hooks: hooks, output: output, commands: commands, input: input,
			media: opts.Media, messages: store.Messages(), mediaRows: store.Media(), logger: logger,
		},
		background: &backgroundRunner{
			sessions: sessions, sessionRows: store.Sessions(), identity: identity, execution: execution,
			preloader: opts.ToolPreloader, toolState: opts.ToolState,
			sandboxRoot: filepath.Clean(strings.TrimSpace(opts.SandboxRoot)), auditLogger: auditLogger,
		},
		fileCommands: &fileCommandPreparer{
			service: opts.FileRollback, sessions: sessions, sessionRows: store.Sessions(),
			turns: turns, requests: requests, identity: identity,
		},
		execution: execution, identity: identity, hooks: hooks, output: output, status: status,
		completion: newCompletion(opts.Commands, sessions, turns, store, identity, opts.ToolRegistry, opts.ToolPreloader), signals: signals,
	}, nil
}
