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
	"elbot/internal/delivery/dispatch"
	"elbot/internal/hook"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/notification"
	"elbot/internal/platform"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
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
	dispatcher         *dispatch.Router
	notifications      *notification.Manager
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
	securityPolicy     *security.Policy
	contexts           *contextmgr.Service
	toolState          *toolrun.StateService
	hooks              hookRunner
	hookRuntime        HookRouter
	statusMu           sync.Mutex
	runtimeStatus      map[string]runtimestatus.Snapshot
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
	actorID                 string
	scopeID                 string
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
		dispatcher:              opts.Dispatcher,
		notifications:           opts.Notifications,
		models:                  opts.Models,
		store:                   store,
		media:                   opts.Media,
		sessions:                sessions,
		requests:                requests,
		turns:                   turns,
		commands:                opts.Commands,
		soul:                    promptSoul,
		residentMemory:          opts.ResidentMemoryStore,
		securityPolicy:          policy,
		contexts:                opts.Contexts,
		toolState:               opts.ToolState,
		hooks:                   hookManager,
		hookRuntime:             opts.HookRuntime,
		runtimeStatus:           map[string]runtimestatus.Snapshot{},
		autoConfirmSession:      map[string]bool{},
		autoConfirmTools:        map[string]map[string]bool{},
		visionFallbackNotified:  map[string]bool{},
		responseTimeout:         responseTimeout(llmRequestConfig),
		userConfirmationTimeout: defaultUserConfirmationTimeout,

		idleExpiration: sessionIdleExpirationConfig(opts.SessionIdleExpiration),
		sandboxRoot:    filepath.Clean(strings.TrimSpace(opts.SandboxRoot)),
		actorID:        "cli:local",
		scopeID:        "local",
	}
	a.toolRuntime = newToolRuntimeState()
	a.toolRuntime.manager = opts.ToolRunner
	a.toolRuntime.registry = opts.ToolRegistry
	a.toolRuntime.fileRollback = opts.FileRollback
	sessions.SetForegroundActivation(a.adoptForeground)
	sessions.SetActivitySource(func() []string {
		var ids []string
		for _, active := range turns.SnapshotAll() {
			if active.Phase != turn.PhaseIdle {
				ids = append(ids, active.SessionID)
			}
		}
		return ids
	})
	if opts.Logs != nil {
		a.SetLogManager(opts.Logs)
	}
	if defaultManager, ok := opts.HookManager.(*hook.DefaultManager); ok {
		defaultManager.SetWakeupFunc(a.hookWakeup)
		defaultManager.SetObserver(a.observeHookRun)
	}
	if opts.ToolRegistry != nil {
		a.toolRuntime.provider = toolRunPromptProvider{agent: a}
	} else if opts.ToolProvider != nil {
		a.SetToolProvider(opts.ToolProvider)
	}
	a.SetToolConfig(opts.ToolsConfig)
	a.SetToolTagConfig(opts.ToolTagsPath, opts.ToolTags)
	a.rebuildSystemPrompt()
	a.commandExecutor = &commandExecutor{
		router:        a.commands,
		sessions:      a.sessions,
		turns:         a.turns,
		scope:         a.scope,
		compactActive: a.compactActive,
		sendChat:      a.sendChat,
		sendNotice: func(ctx context.Context, text string) error {
			return a.sendNotice(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text(text)}})
		},
		audit:         a.audit,
		handleAppend:  a.handleAppendConfirmationInput,
		handleRisk:    a.handleRiskConfirmationInput,
		continueInput: a.continueCommandInput,
	}
	a.completion = completion.NewService(
		completion.RiskConfirmationSource{Router: a.commands, Sessions: a.sessions, Turns: a.turns, Scope: a.scope, CommandNames: riskConfirmationCommandNames()},
		completion.ForkMessageSource{Router: a.commands, Sessions: a.sessions, Store: a.store, Scope: a.scope},
		completion.ToolDirectiveSource{
			Registry:       func() *tool.Registry { return a.toolRuntime.registry },
			Actor:          a.actor,
			Policy:         func() *security.Policy { return a.securityPolicy },
			Tags:           a.completionToolTags,
			ToolNamesByTag: a.completionToolNamesByTag,
		},
		completion.RouterSource{Router: a.commands, Actor: a.actor},
	)

	return a, nil
}

type staticSoulProvider struct {
	Prompt string
}

func (p staticSoulProvider) SystemPrompt(context.Context, string) (string, error) {
	return p.Prompt, nil
}
