package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
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
	platform           platform.PlatformAdapter
	models             *modelmgr.Service
	store              storage.Store
	media              *media.Manager
	sessions           *session.Service
	requests           *request.Manager
	turns              *turn.Manager
	commands           *command.Router
	commandExecutor    *commandExecutor
	completion         *completion.Service
	soul               SoulProvider
	residentMemory     *resident.Store
	promptBuilder      PromptBuilder
	toolRuntime        toolRuntimeState
	contexts           *contextmgr.Service
	toolState          *toolrun.StateService
	identity           *identityResolver
	hooks              *hookBridge
	status             *statusRecorder
	output             *outputSender
	replies            *replyCommitter
	view               executionView
	idleExpiration     session.IdleExpirationConfig
	sandboxRoot        string
	logger             *slog.Logger
	auditLogger        *slog.Logger
	autoConfirmMu      sync.Mutex
	autoConfirmSession map[string]bool
	autoConfirmTools   map[string]map[string]bool
	visionFallbackMu   sync.Mutex

	visionFallbackNotified  map[string]bool
	responseTimeout         time.Duration
	userConfirmationTimeout time.Duration
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
		platform:                p,
		models:                  opts.Models,
		store:                   store,
		media:                   opts.Media,
		sessions:                sessions,
		requests:                requests,
		turns:                   turns,
		commands:                opts.Commands,
		soul:                    promptSoul,
		residentMemory:          opts.ResidentMemoryStore,
		contexts:                opts.Contexts,
		toolState:               opts.ToolState,
		autoConfirmSession:      map[string]bool{},
		autoConfirmTools:        map[string]map[string]bool{},
		visionFallbackNotified:  map[string]bool{},
		responseTimeout:         responseTimeout(llmRequestConfig),
		userConfirmationTimeout: defaultUserConfirmationTimeout,

		idleExpiration: sessionIdleExpirationConfig(opts.SessionIdleExpiration),
		sandboxRoot:    filepath.Clean(strings.TrimSpace(opts.SandboxRoot)),
	}

	a.identity = &identityResolver{platformName: p.Name(), actorID: "cli:local", scopeID: "local", policy: policy}
	a.hooks = &hookBridge{
		manager: hookManager, router: opts.HookRuntime, requests: requests,
		identity: a.identity, media: opts.Media, notifications: opts.Notifications, dispatcher: opts.Dispatcher,
	}
	a.status = &statusRecorder{}
	a.output = &outputSender{dispatcher: opts.Dispatcher, notifications: opts.Notifications, hooks: a.hooks, identity: a.identity}
	a.view = executionView{sessions: store.Sessions()}
	a.replies = &replyCommitter{messages: store.Messages(), output: a.output}
	a.toolRuntime = newToolRuntimeState()
	a.toolRuntime.manager = opts.ToolRunner
	a.toolRuntime.preloader = opts.ToolPreloader
	a.toolRuntime.registry = opts.ToolRegistry
	a.toolRuntime.fileRollback = opts.FileRollback
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
		compactActive: a.compactActive,
		sendChat:      a.output.SendChat,
		sendNotice: func(ctx context.Context, text string) error {
			return a.output.SendNotice(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text(text)}})
		},
		audit:         a.audit,
		handleAppend:  a.handleAppendConfirmationInput,
		handleRisk:    a.handleRiskConfirmationInput,
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
