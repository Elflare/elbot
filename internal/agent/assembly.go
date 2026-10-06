package agent

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	chatroute "elbot/internal/agent/chat"
	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	"elbot/internal/agent/routes"
	"elbot/internal/contextmgr"
	"elbot/internal/hook"
	"elbot/internal/llm"
)

func New(ctx context.Context, cfg Config, deps Dependencies) (*Agent, error) {
	if err := validateConstruction(ctx, cfg, deps); err != nil {
		return nil, err
	}
	store := deps.Store
	requests, turns, sessions := deps.Requests, deps.Turns, deps.Sessions
	var logger, auditLogger *slog.Logger
	if deps.Logs != nil {
		logger, auditLogger = deps.Logs.Runtime(), deps.Logs.Audit()
	}
	hookManager := hookRunner(deps.HookManager)
	if hookManager == nil {
		hookManager = hook.NoopManager{}
	}
	signals := agentevents.NewSignals()
	identity := &identityResolver{platformName: deps.Platform.Name(), actorID: "cli:local", scopeID: "local", policy: deps.SecurityPolicy}
	hooks := &hookBridge{
		manager: hookManager, router: deps.HookRuntime, requests: requests,
		identity: identity, media: deps.Media, failed: signals.HookFailed, dispatcher: deps.Dispatcher, logger: logger,
	}
	status := &statusRecorder{turns: turns, changed: signals.StatusChanged}
	output := &outputSender{dispatcher: deps.Dispatcher, notifications: deps.Notifications, hooks: hooks, identity: identity, logger: logger}
	view := dialogue.ExecutionView{Sessions: store.Sessions()}
	replies := &dialogue.ReplyCommitter{Messages: store.Messages(), Output: output, Delivered: signals.ReplyDelivered, Committed: signals.ReplyCommitted}
	waitPolicy := &confirmationPolicy{identity: identity, idleExpiration: sessionIdleExpirationConfig(cfg.SessionIdleExpiration), userConfirmationTimeout: defaultUserConfirmationTimeout}
	confirmations := &confirmationCoordinator{
		sessions: sessions, requests: requests, turns: turns, commands: deps.Commands,
		identity: identity, output: output, policy: waitPolicy, changed: signals.ConfirmationChanged,
		autoConfirmSession: map[string]bool{}, autoConfirmTools: map[string]map[string]bool{},
	}
	toolRuntime := &toolRuntimeState{
		manager: deps.ToolRunner, registry: deps.ToolRegistry,
		fileRollback: deps.FileRollback, provider: dialogue.NoopToolSchemaProvider{}, defaultProvider: true,
	}
	if deps.ToolRegistry != nil {
		toolRuntime.provider = toolRunPromptProvider{tools: toolRuntime.manager, identity: identity}
	} else if deps.ToolProvider != nil {
		toolRuntime.provider, toolRuntime.defaultProvider = deps.ToolProvider, false
	}
	toolDeps := &toolRunDeps{
		hooks: hooks, requests: requests, turns: turns, identity: identity, media: deps.Media,
		state: deps.ToolState, runtime: toolRuntime, sessions: sessions, store: store,
		confirmations: confirmations, view: view, completed: signals.ToolCallCompleted, denied: signals.ToolDenied,
	}
	tools := &dialogue.ToolExecutor{Manager: deps.ToolRunner, State: deps.ToolState, Registry: deps.ToolRegistry,
		Provider: toolRuntime.provider, DefaultProvider: toolRuntime.defaultProvider, Identity: identity, Deps: toolDeps, MaxRounds: cfg.ToolsConfig.MaxRoundsPerTurn}
	messages := &dialogue.MessageStore{Repository: store.Messages(), Media: deps.Media, Failed: signals.PersistenceFailed}
	preparer := &dialogue.Preparer{Contexts: deps.Contexts, Media: deps.Media, Identity: identity, Hooks: hooks, Tools: tools, InputReceived: signals.UserInputReceived}
	calls := &dialogue.CallProcessor{Messages: messages, Media: deps.Media, Hooks: hooks, Identity: identity, Tools: tools, Completed: signals.ModelCallCompleted, Vision: signals.VisionFallbackUsed}
	chat := &chatroute.Loop{Logger: logger, Contexts: deps.Contexts, Models: deps.Models, Turns: turns, View: view, Preparer: preparer, Tools: tools, Messages: messages, Caller: &chatroute.Caller{Calls: calls}, PromptBuilder: chatroute.PromptBuilder{System: buildSystemPrompt(cfg.SoulPath, deps.ResidentMemoryStore, toolRuntime.provider, deps.ToolPreloader)}}
	compactor := &chatroute.Compactor{Store: store, Models: deps.Models, Contexts: deps.Contexts, Loader: contextmgr.Loader{Store: store}}
	if err := deps.Routes.Register(routes.Route{Protocol: llm.ProtocolChat, Loop: chat, Compactor: compactor}); err != nil {
		return nil, err
	}
	if err := deps.Routes.Seal(); err != nil {
		return nil, err
	}
	if err := deps.Contexts.CheckCompaction(llm.ProtocolChat); err != nil {
		return nil, fmt.Errorf("context compaction wiring: %w", err)
	}
	runner := &dialogue.Runner{Routes: deps.Routes, Preparer: preparer, Messages: messages, Replies: replies, Turns: turns, View: view}
	execution := &executionCoordinator{
		sessions: sessions, sessionRows: store.Sessions(), turns: turns, requests: requests, contexts: deps.Contexts,
		models: deps.Models, dialogue: runner, identity: identity, view: view, output: output, status: status,
		waitPolicy: waitPolicy, responseTimeout: responseTimeout(cfg.LLMRequestConfig), persistenceFailed: signals.PersistenceFailed, timedOut: signals.TurnTimedOut, logger: logger,
		appendWaits: newAppendWaitLifecycle(ctx),
	}
	input := &inputCoordinator{
		sessions: sessions, sessionRows: store.Sessions(), turns: turns, identity: identity,
		hooks: hooks, output: output, execution: execution, confirmations: confirmations,
		registry: deps.ToolRegistry, preloader: deps.ToolPreloader, toolState: deps.ToolState, waitPolicy: waitPolicy, auditLogger: auditLogger,
	}
	commands := &commandExecutor{
		router: deps.Commands, sessions: sessions, turns: turns, identity: identity,
		execution: execution, output: output, confirmations: confirmations, input: input, auditLogger: auditLogger,
	}
	return &Agent{
		message: &messageHandler{
			identity: identity, hooks: hooks, output: output, commands: commands, input: input,
			media: deps.Media, messages: store.Messages(), mediaRows: store.Media(), logger: logger,
		},
		background: &backgroundRunner{
			sessions: sessions, sessionRows: store.Sessions(), identity: identity, execution: execution,
			preloader: deps.ToolPreloader, toolState: deps.ToolState,
			sandboxRoot: filepath.Clean(strings.TrimSpace(cfg.SandboxRoot)), auditLogger: auditLogger,
		},
		fileCommands: &fileCommandPreparer{
			service: deps.FileRollback, sessions: sessions, sessionRows: store.Sessions(),
			turns: turns, requests: requests, identity: identity,
		},
		execution: execution, identity: identity, hooks: hooks, output: output, status: status,
		completion: newCompletion(deps.Commands, sessions, turns, store, identity, deps.ToolRegistry, deps.ToolPreloader), signals: signals,
	}, nil
}
