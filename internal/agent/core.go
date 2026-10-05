package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/platform"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

// Agent is the minimal agent core that handles messages and commands.
type Agent struct {
	platform        platform.PlatformAdapter
	models          *modelmgr.Service
	store           storage.Store
	media           *media.Manager
	sessions        *session.Service
	requests        *request.Manager
	turns           *turn.Manager
	commands        *command.Router
	commandExecutor *commandExecutor
	completion      *completion.Service
	soul            SoulProvider
	residentMemory  *resident.Store
	toolRuntime     toolRuntimeState
	contexts        *contextmgr.Service
	toolState       *toolrun.StateService
	identity        *identityResolver
	hooks           *hookBridge
	status          *statusRecorder
	signals         Signals
	output          *outputSender
	execution       *executionCoordinator
	chat            *chatRunner
	caller          *modelCaller
	replies         *replyCommitter
	view            executionView
	waitPolicy      *confirmationPolicy
	confirmations   *confirmationCoordinator
	toolDeps        *toolRunDeps
	sandboxRoot     string
	logger          *slog.Logger
	auditLogger     *slog.Logger
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
	p := opts.Platform
	store := opts.Store
	soulPath := opts.SoulPath
	llmRequestConfig := opts.LLMRequestConfig
	promptSoul := SoulProvider(staticSoulProvider{Prompt: "You are a helpful assistant."})
	if soulPath != "" {
		promptSoul = &FileSoulProvider{Path: soulPath}
	}
	requests, turns, sessions := opts.Requests, opts.Turns, opts.Sessions
	policy := opts.SecurityPolicy
	hookManager := hookRunner(opts.HookManager)
	if hookManager == nil {
		hookManager = hook.NoopManager{}
	}
	a := &Agent{
		platform:       p,
		models:         opts.Models,
		store:          store,
		media:          opts.Media,
		sessions:       sessions,
		requests:       requests,
		turns:          turns,
		commands:       opts.Commands,
		soul:           promptSoul,
		residentMemory: opts.ResidentMemoryStore,
		contexts:       opts.Contexts,
		toolState:      opts.ToolState,

		sandboxRoot: filepath.Clean(strings.TrimSpace(opts.SandboxRoot)),
	}

	a.signals = newSignals()
	a.identity = &identityResolver{platformName: p.Name(), actorID: "cli:local", scopeID: "local", policy: policy}
	a.hooks = &hookBridge{
		manager: hookManager, router: opts.HookRuntime, requests: requests,
		identity: a.identity, media: opts.Media, failed: a.signals.HookFailed, dispatcher: opts.Dispatcher,
	}
	a.status = &statusRecorder{turns: turns, changed: a.signals.StatusChanged}
	a.output = &outputSender{dispatcher: opts.Dispatcher, notifications: opts.Notifications, hooks: a.hooks, identity: a.identity}
	a.view = executionView{sessions: store.Sessions()}
	a.replies = &replyCommitter{messages: store.Messages(), output: a.output, delivered: a.signals.ReplyDelivered, committed: a.signals.ReplyCommitted}
	a.waitPolicy = &confirmationPolicy{identity: a.identity, idleExpiration: sessionIdleExpirationConfig(opts.SessionIdleExpiration), userConfirmationTimeout: defaultUserConfirmationTimeout}
	a.confirmations = &confirmationCoordinator{
		sessions: sessions, requests: requests, turns: turns, commands: a.commands,
		identity: a.identity, output: a.output, policy: a.waitPolicy, changed: a.signals.ConfirmationChanged,
		autoConfirmSession: map[string]bool{}, autoConfirmTools: map[string]map[string]bool{},
	}
	a.toolRuntime = newToolRuntimeState()
	a.toolRuntime.manager = opts.ToolRunner
	a.toolRuntime.preloader = opts.ToolPreloader
	a.toolRuntime.registry = opts.ToolRegistry
	a.toolRuntime.fileRollback = opts.FileRollback
	a.toolDeps = &toolRunDeps{
		hooks: a.hooks, requests: requests, turns: turns, identity: a.identity, media: opts.Media,
		state: a.toolState, runtime: &a.toolRuntime, sessions: sessions, store: store,
		confirmations: a.confirmations, view: a.view, completed: a.signals.ToolCallCompleted, denied: a.signals.ToolDenied,
	}
	a.caller = &modelCaller{
		messages: store.Messages(), media: opts.Media, hooks: a.hooks, identity: a.identity,
		toolState: a.toolState, toolRuntime: &a.toolRuntime, completed: a.signals.ModelCallCompleted, vision: a.signals.VisionFallbackUsed, persistenceFailed: a.signals.PersistenceFailed,
	}
	a.chat = &chatRunner{
		messages: store.Messages(), media: opts.Media, contexts: a.contexts, models: a.models, turns: turns, identity: a.identity,
		hooks: a.hooks, view: a.view, toolRuntime: &a.toolRuntime, toolState: a.toolState,
		toolDeps: a.toolDeps, caller: a.caller, replies: a.replies, inputReceived: a.signals.UserInputReceived, persistenceFailed: a.signals.PersistenceFailed,
	}
	a.execution = &executionCoordinator{
		sessions: sessions, sessionRows: store.Sessions(), turns: turns, requests: requests, contexts: a.contexts,
		models: a.models, chat: a.chat, identity: a.identity, view: a.view, output: a.output, status: a.status,
		waitPolicy: a.waitPolicy, responseTimeout: responseTimeout(llmRequestConfig), persistenceFailed: a.signals.PersistenceFailed, timedOut: a.signals.TurnTimedOut,
	}
	if opts.Logs != nil {
		a.SetLogManager(opts.Logs)
	}
	if opts.ToolRegistry != nil {
		a.toolRuntime.provider = toolRunPromptProvider{tools: a.toolRuntime.manager, identity: a.identity}
	} else if opts.ToolProvider != nil {
		a.SetToolProvider(opts.ToolProvider)
	}
	a.SetToolConfig(opts.ToolsConfig)
	a.rebuildSystemPrompt()
	a.commandExecutor = &commandExecutor{
		router:        a.commands,
		sessions:      a.sessions,
		turns:         a.turns,
		scope:         a.identity.Scope,
		compactActive: a.execution.compactActive,
		sendChat:      a.output.SendChat,
		sendNotice: func(ctx context.Context, text string) error {
			return a.output.SendNotice(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text(text)}})
		},
		audit:         a.audit,
		handleAppend:  a.execution.ResumeAppend,
		handleRisk:    a.confirmations.SubmitResponse,
		continueInput: a.continueCommandInput,
	}
	a.completion = completion.NewService(
		completion.RiskConfirmationSource{Router: a.commands, Sessions: a.sessions, Turns: a.turns, Scope: a.identity.Scope, CommandNames: riskConfirmationCommandNames()},
		completion.ForkMessageSource{Router: a.commands, Sessions: a.sessions, Store: a.store, Scope: a.identity.Scope},
		completion.ToolDirectiveSource{
			Registry: func() *tool.Registry { return a.toolRuntime.registry },
			Actor:    a.identity.Actor,
			Policy:   func() *security.Policy { return a.identity.policy },
			Tags: func(ctx context.Context, _ *tool.Registry, actor security.Actor, policy *security.Policy) []string {
				return a.toolRuntime.preloader.Tags(security.WithActor(security.WithPolicy(ctx, policy), actor))
			},
			ToolNamesByTag: func(ctx context.Context, _ *tool.Registry, tag string, allowed func(tool.Tool) bool) []string {
				return a.toolRuntime.preloader.ToolNamesByTag(ctx, tag, allowed)
			},
		},
		completion.RouterSource{Router: a.commands, Actor: a.identity.Actor},
	)

	return a, nil
}

type staticSoulProvider struct {
	Prompt string
}

func (p staticSoulProvider) SystemPrompt(context.Context, string) (string, error) {
	return p.Prompt, nil
}
