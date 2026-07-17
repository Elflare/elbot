package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	agentcommands "elbot/internal/agent/commands"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/platform"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

// Agent is the minimal agent core that handles messages and commands.
type Agent struct {
	platform           platform.PlatformAdapter
	platformSenders    map[string]delivery.MessageSender
	modelRuntime       modelRuntimeState
	statePath          string
	stateModTime       time.Time
	store              storage.Store
	sessions           *session.Service
	requests           *request.Manager
	turns              *turn.Manager
	commands           *command.Router
	commandExecutor    *commandExecutor
	completion         *completion.Service
	titleGen           *titleGenerator
	soul               SoulProvider
	promptBuilder      PromptBuilder
	toolRuntime        toolRuntimeState
	securityPolicy     *security.Policy
	contextRuntime     contextRuntimeState
	hooks              hookRunner
	hookRuntime        hookRouter
	outputs            delivery.Manager
	namingModelMu      sync.RWMutex
	namingModel        config.ModelSelection
	statusMu           sync.Mutex
	runtimeStatus      map[string]runtimestatus.Snapshot
	sessionCommands    *agentcommands.SessionCommandState
	idleExpiration     session.IdleExpirationConfig
	sandboxRoot        string
	logger             *slog.Logger
	auditLogger        *slog.Logger
	logReader          logging.Reader
	autoConfirmMu      sync.Mutex
	autoConfirmSession map[string]bool
	autoConfirmTools   map[string]map[string]bool
	visionFallbackMu   sync.Mutex

	visionFallbackNotified map[string]bool
	responseTimeout        time.Duration
	discoveredTools        map[string]map[string]llm.ToolSchema
	actorID                string
	scopeID                string
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
	return NewWithOptions(Options{
		Platform:         p,
		Client:           client,
		ModeModels:       modeModels,
		Providers:        map[string]config.ProviderConfig{"default": provider},
		Store:            store,
		CommandPrefixes:  prefixes,
		SessionConfig:    session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork},
		LLMRequestConfig: config.Default().LLMRequest,
	})
}

func responseTimeout(cfg config.LLMRequestConfig) time.Duration {
	if cfg.ResponseTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.ResponseTimeoutSeconds) * time.Second
}

func NewWithOptions(opts Options) *Agent {
	p := opts.Platform
	client := opts.Client
	modeModels := opts.ModeModels
	providers := opts.Providers
	statePath := opts.StatePath
	store := opts.Store
	prefixes := opts.CommandPrefixes
	sessionCfg := opts.SessionConfig
	namingSelection := opts.NamingSelection
	namingClient := opts.NamingClient
	namingModel := opts.NamingModel
	namingNotifier := opts.NamingNotifier
	soulPath := opts.SoulPath
	llmRequestConfig := opts.LLMRequestConfig
	hookService := opts.HookService
	workModel := modeModels[storage.SessionModeWork]
	if workModel.Provider == "" || workModel.Model == "" {
		panic("mode_models.work provider/model is required")
	}
	provider, ok := providers[workModel.Provider]
	if !ok {
		panic("provider not found: " + workModel.Provider)
	}
	titleGen := &titleGenerator{primary: client, primaryModel: workModel.Model, naming: namingClient, namingModel: namingModel}
	clients := map[string]llm.LLM{workModel.Provider: client}
	promptSoul := SoulProvider(staticSoulProvider{Prompt: "You are a helpful assistant."})
	if soulPath != "" {
		promptSoul = &FileSoulProvider{Path: soulPath}
	}
	stateModTime := initialStateModTime(statePath)
	requests := request.NewManager(0)
	turns := turn.NewManager()
	sessions := session.NewServiceWithConfig(store, sessionCfg, titleGen, namingNotifier)
	sessionCommands := agentcommands.NewSessionCommandState(config.Default().View.SessionListPageSize, 30)
	a := &Agent{
		platform:               p,
		platformSenders:        map[string]delivery.MessageSender{},
		modelRuntime:           newModelRuntimeState(client, workModel.Model, workModel.Provider, provider, llmRequestConfig, providers, modeModels, clients),
		statePath:              statePath,
		stateModTime:           stateModTime,
		store:                  store,
		sessions:               sessions,
		requests:               requests,
		turns:                  turns,
		commands:               command.NewRouter(prefixes),
		soul:                   promptSoul,
		securityPolicy:         security.DefaultPolicy(),
		contextRuntime:         newContextRuntimeState(store, sessions, requests, turns),
		hooks:                  hook.NoopManager{},
		outputs:                delivery.NewManager(nil, nil),
		namingModel:            namingSelection,
		runtimeStatus:          map[string]runtimestatus.Snapshot{},
		autoConfirmSession:     map[string]bool{},
		autoConfirmTools:       map[string]map[string]bool{},
		visionFallbackNotified: map[string]bool{},
		responseTimeout:        responseTimeout(llmRequestConfig),

		discoveredTools: map[string]map[string]llm.ToolSchema{},

		sessionCommands: sessionCommands,
		idleExpiration:  sessionIdleExpirationConfig(config.Default().Session.IdleExpiration),
		sandboxRoot:     config.Default().Sandbox.Root,
		actorID:         "cli:local",
		scopeID:         "local",
	}
	a.rebuildSystemPrompt()
	a.attachLLMRetryNotifier(client, workModel.Provider)
	if namingClient != nil && namingClient != client {
		a.attachLLMRetryNotifier(namingClient, namingSelection.Provider)
	}
	if p != nil {
		a.platformSenders[p.Name()] = p
	}
	a.SetContextOptions(config.Default().Context, config.ModelMetadataConfig{}, providers, config.ModelSelection{})
	if err := agentcommands.RegisterDefaultModules(a.commands, agentcommands.Deps{
		Router:        a.commands,
		Sessions:      a.sessions,
		Requests:      a.requests,
		Turns:         a.turns,
		Store:         a.store,
		Scope:         a.scope,
		Models:        a,
		Compact:       a,
		ContextStatus: a,
		Tools:         a,
		Hooks:         hookService,
		SessionState:  sessionCommands,
		Audit:         a.audit,
		Logs:          a,
		RuntimeStatus: a.runtimeStatusForSession,
	}); err != nil {
		panic(err)
	}
	a.commandExecutor = &commandExecutor{
		router:        a.commands,
		sessions:      a.sessions,
		turns:         a.turns,
		scope:         a.scope,
		compactActive: a.compactActive,
		sendChat:      a.sendChat,
		sendNotice: func(ctx context.Context, text string) error {
			return a.sendNotice(ctx, delivery.Target{}, []delivery.Output{delivery.Text(text)})
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
		completion.RouterSource{Router: a.commands},
	)

	return a
}

type staticSoulProvider struct {
	Prompt string
}

func (p staticSoulProvider) SystemPrompt(context.Context, string) (string, error) {
	return p.Prompt, nil
}

func cloneModeModels(models map[string]config.ModelSelection) map[string]config.ModelSelection {
	out := map[string]config.ModelSelection{}
	for mode, model := range models {
		out[mode] = model
	}
	return out
}
