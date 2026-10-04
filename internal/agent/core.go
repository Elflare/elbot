package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentcommands "elbot/internal/agent/commands"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
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
	platformSenders    map[string]delivery.MessageSender
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
	outputs            delivery.Manager
	statusMu           sync.Mutex
	runtimeStatus      map[string]runtimestatus.Snapshot
	sessionCommands    *agentcommands.SessionCommandState
	idleExpiration     session.IdleExpirationConfig
	mediaRetentionDays int
	sandboxRoot        string
	logger             *slog.Logger
	auditLogger        *slog.Logger
	logReader          logging.Reader
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

// New creates a new Agent.
func New(p platform.PlatformAdapter, client llm.LLM, model string, provider config.ProviderConfig, store storage.Store) *Agent {
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "default", Model: model},
		storage.SessionModeChat: {Provider: "default", Model: model},
	}
	return NewWithPrefixes(p, client, modeModels, provider, store, []string{"/"})
}

func NewWithPrefixes(p platform.PlatformAdapter, client llm.LLM, modeModels map[string]config.ModelSelection, provider config.ProviderConfig, store storage.Store, prefixes []string) *Agent {
	defaults := config.Default()
	models, err := modelmgr.New(modelmgr.Options{Clients: map[string]llm.LLM{"default": client}, Providers: map[string]config.ProviderConfig{"default": provider}, ModeModels: modeModels, DefaultMode: storage.SessionModeWork})
	if err != nil {
		panic(err)
	}
	agent, err := NewWithOptions(Options{
		Platform:              p,
		Models:                models,
		Providers:             map[string]config.ProviderConfig{"default": provider},
		Store:                 store,
		CommandPrefixes:       prefixes,
		SessionConfig:         session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork},
		LLMRequestConfig:      defaults.LLMRequest,
		SecurityPolicy:        security.DefaultPolicy(),
		ContextConfig:         defaults.Context,
		SessionListPageSize:   defaults.View.SessionListPageSize,
		CleanupRetentionDays:  30,
		MediaRetentionDays:    defaults.Maintenance.SandboxCleanup.RetentionDays,
		SessionIdleExpiration: defaults.Session.IdleExpiration,
		SandboxRoot:           defaults.Sandbox.Root,
		ToolsConfig:           defaults.Tools,
	})
	if err != nil {
		panic(err)
	}
	return agent
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
	prefixes := opts.CommandPrefixes
	sessionCfg := opts.SessionConfig
	namingNotifier := opts.NamingNotifier
	soulPath := opts.SoulPath
	llmRequestConfig := opts.LLMRequestConfig
	hookService := opts.HookService
	titleGen := &titleGenerator{models: opts.Models}
	promptSoul := SoulProvider(staticSoulProvider{Prompt: "You are a helpful assistant."})
	if soulPath != "" {
		promptSoul = &FileSoulProvider{Path: soulPath}
	}
	requests := request.NewManager(0)
	turns := turn.NewManager()
	sessions := session.NewServiceWithConfig(store, sessionCfg, titleGen, namingNotifier)
	sessionCommands := agentcommands.NewSessionCommandState(opts.SessionListPageSize, opts.CleanupRetentionDays)
	policy := opts.SecurityPolicy
	hookManager := hookRunner(opts.HookManager)
	if hookManager == nil {
		hookManager = hook.NoopManager{}
	}
	outputs := opts.OutputManager
	if outputs.Sender == nil && outputs.Logger == nil {
		outputs = delivery.NewManager(nil, nil)
	}
	a := &Agent{
		platform:                p,
		platformSenders:         map[string]delivery.MessageSender{},
		models:                  opts.Models,
		store:                   store,
		media:                   opts.Media,
		mediaRetentionDays:      opts.MediaRetentionDays,
		sessions:                sessions,
		requests:                requests,
		turns:                   turns,
		commands:                command.NewRouter(prefixes),
		soul:                    promptSoul,
		residentMemory:          opts.ResidentMemoryStore,
		securityPolicy:          policy,
		contexts:                opts.Contexts,
		toolState:               opts.ToolState,
		hooks:                   hookManager,
		hookRuntime:             opts.HookRuntime,
		outputs:                 outputs,
		runtimeStatus:           map[string]runtimestatus.Snapshot{},
		autoConfirmSession:      map[string]bool{},
		autoConfirmTools:        map[string]map[string]bool{},
		visionFallbackNotified:  map[string]bool{},
		responseTimeout:         responseTimeout(llmRequestConfig),
		userConfirmationTimeout: defaultUserConfirmationTimeout,

		sessionCommands: sessionCommands,
		idleExpiration:  sessionIdleExpirationConfig(opts.SessionIdleExpiration),
		sandboxRoot:     filepath.Clean(strings.TrimSpace(opts.SandboxRoot)),
		actorID:         "cli:local",
		scopeID:         "local",
	}
	if a.contexts == nil {
		a.contexts = contextmgr.New(contextmgr.Options{Store: store, Models: opts.Models, Config: opts.ContextConfig, Metadata: opts.ModelMetadata, Providers: opts.Providers})
	}
	if a.toolState == nil {
		a.toolState = toolrun.NewStateService(store)
	}
	a.toolRuntime = newToolRuntimeState()
	a.toolRuntime.manager = toolrun.NewManager(nil, policy)
	a.toolRuntime.manager.Media = opts.Media
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
	if opts.ToolRegistry != nil || opts.Skills != nil {
		a.SetToolRuntime(opts.ToolRegistry, opts.Skills)
	} else if opts.ToolProvider != nil {
		a.SetToolProvider(opts.ToolProvider)
	}
	a.SetToolConfig(opts.ToolsConfig)
	a.SetToolTagConfig(opts.ToolTagsPath, opts.ToolTags)
	a.rebuildSystemPrompt()
	opts.Models.SetRetryNotifier(a.notifyLLMRetry)
	if p != nil {
		a.platformSenders[p.Name()] = p
	}
	if err := agentcommands.RegisterDefaultModules(a.commands, agentcommands.Deps{
		Doctor:        opts.Doctor,
		Router:        a.commands,
		Sessions:      a.sessions,
		Requests:      a.requests,
		Turns:         a.turns,
		Store:         a.store,
		Scope:         a.scope,
		Models:        opts.Models,
		Compact:       a,
		ContextStatus: a,
		Tools:         a,
		FileRollback:  a,
		Hooks:         hookService,
		SessionState:  sessionCommands,
		Audit:         a.audit,
		Logs:          a,
		RuntimeStatus: a.runtimeStatusForSession,
	}); err != nil {
		return nil, err
	}
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
