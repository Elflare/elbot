package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	chatroute "elbot/internal/agent/chat"
	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	responseroute "elbot/internal/agent/responses"
	"elbot/internal/contextmgr"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

func New(ctx context.Context, cfg Config, deps Dependencies) (*Agent, error) {
	if err := validateConstruction(ctx, cfg, deps); err != nil {
		return nil, err
	}
	store := deps.Store
	requests, turns, sessions := deps.Requests, deps.Turns, deps.Sessions
	hookManager := hookRunner(deps.HookManager)
	if hookManager == nil {
		hookManager = hook.NoopManager{}
	}
	signals := agentevents.NewSignals()
	identity := &identityResolver{platformName: deps.Platform.Name(), actorID: "cli:local", scopeID: "local", policy: deps.SecurityPolicy}
	hooks := &hookBridge{manager: hookManager, router: deps.HookRuntime, requests: requests, identity: identity, media: deps.Media, failed: signals.HookFailed, dispatcher: deps.Dispatcher}
	status := &statusRecorder{turns: turns, changed: signals.StatusChanged}
	output := &outputSender{dispatcher: deps.Dispatcher, notifications: deps.Notifications, hooks: hooks, identity: identity}
	view := dialogue.ExecutionView{Sessions: store.Sessions(), Providers: deps.Routes}
	sessions.SetForegroundCheck(func(_ context.Context, source *storage.Session) error {
		return view.CheckSelection(source, deps.Models.ResolveMode(storage.SessionModeWork))
	})
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
	messages := &dialogue.MessageStore{Dialogues: store.Dialogues(), Gate: &dialogue.CommitGate{Sessions: sessions, Turns: turns, View: view}, Media: deps.Media, Failed: signals.PersistenceFailed}
	replies.Persistence = messages.Committer("append_assistant_message")
	tools.Messages = messages
	preparer := &dialogue.Preparer{Contexts: deps.Contexts, Media: deps.Media, Identity: identity, Hooks: hooks, Tools: tools, InputReceived: signals.UserInputReceived}
	calls := &dialogue.CallProcessor{Messages: messages, Media: deps.Media, Hooks: hooks, Identity: identity, Tools: tools, Completed: signals.ModelCallCompleted, Vision: signals.VisionFallbackUsed}
	system := buildSystemPrompt(cfg.SoulPath, deps.ResidentMemoryStore, toolRuntime.provider, deps.ToolPreloader)
	chat := &chatroute.Loop{
		Contexts:      deps.Contexts,
		Models:        deps.Models,
		Turns:         turns,
		View:          view,
		Preparer:      preparer,
		Tools:         tools,
		Messages:      messages,
		Caller:        &chatroute.Caller{Calls: calls},
		PromptBuilder: chatroute.PromptBuilder{System: system},
	}
	nativeContext := &responseroute.Context{Repository: store.Dialogues(), Media: deps.Media}
	response := &responseroute.Loop{
		Repository: store.Dialogues(),
		Context:    nativeContext,
		Models:     deps.Models,
		Turns:      turns,
		View:       view,
		Preparer:   preparer,
		Tools:      tools,
		Messages:   messages,
		Calls:      calls,
		System:     system,
	}
	compactor := &chatroute.Compactor{Store: store, Models: deps.Models, Contexts: deps.Contexts, Loader: contextmgr.Loader{Store: store}}
	nativeCompactor := &responseroute.Compactor{Context: nativeContext, Messages: messages, View: view, System: system, Identity: identity}
	if err := bindProviderRoutes(deps.Routes, deps.Models, chat, response, compactor, nativeCompactor, nativeContext); err != nil {
		return nil, err
	}
	sessions.SetMaterials(deps.Routes)
	if err := deps.Contexts.CheckCompaction(llm.Origin{APIType: llm.APITypeChat}); err != nil {
		return nil, fmt.Errorf("context compaction wiring: %w", err)
	}
	runner := &dialogue.Runner{Routes: deps.Routes, Preparer: preparer, Messages: messages, Replies: replies, Turns: turns, View: view}
	execution := &executionCoordinator{
		sessions:          sessions,
		sessionRows:       store.Sessions(),
		turns:             turns,
		requests:          requests,
		contexts:          deps.Contexts,
		models:            deps.Models,
		dialogue:          runner,
		identity:          identity,
		view:              view,
		output:            output,
		status:            status,
		waitPolicy:        waitPolicy,
		responseTimeout:   responseTimeout(cfg.LLMRequestConfig),
		persistenceFailed: signals.PersistenceFailed,
		timedOut:          signals.TurnTimedOut,
		appendWaits:       newAppendWaitLifecycle(ctx),
	}
	input := &inputCoordinator{
		sessions:      sessions,
		sessionRows:   store.Sessions(),
		turns:         turns,
		identity:      identity,
		hooks:         hooks,
		output:        output,
		execution:     execution,
		confirmations: confirmations,
		registry:      deps.ToolRegistry,
		preloader:     deps.ToolPreloader,
		toolState:     deps.ToolState,
		waitPolicy:    waitPolicy,
	}
	commands := &commandExecutor{router: deps.Commands, sessions: sessions, turns: turns, identity: identity, execution: execution, output: output, confirmations: confirmations, input: input}
	a := &Agent{
		message: &messageHandler{identity: identity, hooks: hooks, output: output, commands: commands, input: input, media: deps.Media, messages: store.Messages(), mediaRows: store.Media()},
		background: &backgroundRunner{
			sessions:    sessions,
			sessionRows: store.Sessions(),
			identity:    identity,
			execution:   execution,
			preloader:   deps.ToolPreloader,
			toolState:   deps.ToolState,
			sandboxRoot: filepath.Clean(strings.TrimSpace(cfg.SandboxRoot)),
		},
		fileCommands: &fileCommandPreparer{
			service: deps.FileRollback, sessions: sessions, sessionRows: store.Sessions(),
			turns: turns, requests: requests, identity: identity,
		},
		execution: execution, identity: identity, hooks: hooks, output: output, status: status,
		completion: newCompletion(deps.Commands, sessions, turns, store, identity, deps.ToolRegistry, deps.ToolPreloader, execution), signals: signals,
	}
	if err := a.connectLogSignals(); err != nil {
		_ = a.Close(ctx)
		return nil, err
	}
	return a, nil
}
