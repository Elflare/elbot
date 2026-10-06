package agent

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	chatroute "elbot/internal/agent/chat"
	"elbot/internal/agent/dialogue"
	"elbot/internal/agent/routes"
	"elbot/internal/command"
	commandbuiltin "elbot/internal/command/builtin"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/fileops"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/notification"
	notificationrules "elbot/internal/notification/rules"
	"elbot/internal/platform"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

// Test configuration is intentionally separate from the production constructor:
// the application supplies already assembled shared services.
type testAgentOptions struct {
	RuntimeContext        context.Context
	Platform              platform.PlatformAdapter
	Media                 *media.Manager
	Models                *modelmgr.Service
	Store                 storage.Store
	Providers             map[string]config.ProviderConfig
	CommandPrefixes       []string
	SessionConfig         session.Config
	SecurityPolicy        *security.Policy
	SandboxRoot           string
	ToolsConfig           config.ToolsConfig
	FileRollback          *fileops.Service
	ToolRegistry          *tool.Registry
	ToolProvider          dialogue.ToolSchemaProvider
	ToolTagsPath          string
	ToolTags              config.ToolTagsConfig
	SessionIdleExpiration *config.SessionIdleExpirationConfig
	HookManager           hook.Manager
	HookRuntime           HookRouter
	Logs                  LogManager
	SoulPath              string
	ResidentMemoryStore   *resident.Store
}

func assembleTestOptions(opts testAgentOptions) testAssembly {
	defaults := config.Default()
	if opts.Platform == nil {
		opts.Platform = &fakePlatform{}
	}
	if opts.SecurityPolicy == nil {
		opts.SecurityPolicy = security.DefaultPolicy()
	}
	if opts.SandboxRoot == "" {
		opts.SandboxRoot = defaults.Sandbox.Root
	}
	if opts.ToolsConfig.MaxRoundsPerTurn <= 0 {
		opts.ToolsConfig = defaults.Tools
	}
	dispatcher := dispatch.New(dispatch.Options{Primary: opts.Platform, Store: opts.Store, Media: opts.Media, MediaRetentionDays: defaults.Maintenance.SandboxCleanup.RetentionDays})
	notices := notification.New(dispatcher, nil, false)
	_, _ = opts.Models.ModelRetrying().Connect(func(ctx context.Context, event modelmgr.ModelRetryingEvent) error {
		notificationrules.ModelRetry(notices)(ctx, event.Provider, event.Retry)
		return nil
	}, signal.ConnectOptions{})
	registry := routes.New()
	optsResult := testAssembly{RuntimeContext: opts.RuntimeContext, Config: Config{
		SoulPath: opts.SoulPath, LLMRequestConfig: defaults.LLMRequest, SessionIdleExpiration: defaults.Session.IdleExpiration, SandboxRoot: opts.SandboxRoot, ToolsConfig: opts.ToolsConfig,
	}, Dependencies: Dependencies{
		Routes: registry, Platform: opts.Platform, Models: opts.Models, Store: opts.Store, Media: opts.Media,
		Sessions: session.NewServiceWithConfig(opts.Store, opts.SessionConfig, session.NewTitleGenerator(opts.Models)),
		Commands: command.NewRouter(opts.CommandPrefixes), Requests: request.NewManager(0), Turns: turn.NewManager(),
		Contexts:  contextmgr.New(contextmgr.Options{Compactors: registry, Store: opts.Store, Models: opts.Models, Config: defaults.Context, Metadata: defaults.ModelMetadata, Providers: opts.Providers}),
		ToolState: toolrun.NewStateService(opts.Store), ToolRunner: toolrun.NewManager(opts.ToolRegistry, opts.SecurityPolicy),
		ToolPreloader: toolrun.NewPreloadService(toolrun.PreloadOptions{Registry: opts.ToolRegistry, TagsPath: opts.ToolTagsPath, Tags: opts.ToolTags}),
		ToolRegistry:  opts.ToolRegistry, FileRollback: opts.FileRollback, Dispatcher: dispatcher, Notifications: notices, SecurityPolicy: opts.SecurityPolicy,
		ToolProvider: opts.ToolProvider, HookManager: opts.HookManager, HookRuntime: opts.HookRuntime, Logs: opts.Logs, ResidentMemoryStore: opts.ResidentMemoryStore,
	}}

	optsResult.ToolRunner.Media = opts.Media
	if opts.SessionIdleExpiration != nil {
		optsResult.SessionIdleExpiration = *opts.SessionIdleExpiration
	}
	return optsResult
}

func mustNewWithOptions(t *testing.T, cfg testAgentOptions, configure ...func(*testAgentOptions)) *Agent {
	t.Helper()
	for _, change := range configure {
		change(&cfg)
	}
	opts := assembleTestOptions(cfg)
	runtimeCtx := opts.RuntimeContext
	if runtimeCtx == nil {
		runtimeCtx = t.Context()
	}
	a, err := New(runtimeCtx, opts.Config, opts.Dependencies)
	if err != nil {
		t.Fatal(err)
	}
	connectTestObservers(a, assembleObserverOptions{notifications: a.output.notifications, dispatcher: a.output.dispatcher})
	opts.Sessions.SetForegroundActivation(a.AdoptForeground)
	opts.Sessions.StartNaming(context.Background())
	ownedSessions := opts.Sessions
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := ownedSessions.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	opts.Sessions.SetActivitySource(func() []string {
		var ids []string
		for _, active := range opts.Turns.SnapshotAll() {
			if active.Phase != turn.PhaseIdle {
				ids = append(ids, active.SessionID)
			}
		}
		return ids
	})
	if err := commandbuiltin.RegisterDefaultModules(opts.Commands, commandbuiltin.Deps{
		Router: opts.Commands, Sessions: opts.Sessions, Requests: opts.Requests, Turns: opts.Turns, Store: opts.Store,
		Scope: a.Scope, Models: opts.Models, Contexts: opts.Contexts, Compact: a,
		Tools: testToolRegistry{opts.ToolRegistry}, FileRollback: opts.FileRollback, PrepareFileContext: a.PrepareFileCommand,
		SessionState: commandbuiltin.NewSessionCommandState(config.Default().View.SessionListPageSize, 30),
		Audit: func(event string, attrs ...any) {
			var logger *slog.Logger
			if opts.Logs != nil {
				logger = opts.Logs.Audit()
			}
			writeAudit(logger, slog.LevelInfo, event, attrs...)
		}, RuntimeStatus: a.RuntimeStatus,
	}); err != nil {
		t.Fatal(err)
	}
	if manager, ok := opts.HookManager.(*hook.DefaultManager); ok {
		manager.SetWakeupFunc(a.HookWakeup)
		manager.SetObserver(a.ObserveHookRun)
	}
	return a
}

func newTestAgent(t *testing.T, p platform.PlatformAdapter, client llm.Client, model string, provider config.ProviderConfig, store storage.Store, configure ...func(*testAgentOptions)) *Agent {
	t.Helper()
	return newTestAgentWithPrefixes(t, p, client, map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "default", Model: model},
		storage.SessionModeChat: {Provider: "default", Model: model},
	}, provider, store, []string{"/"}, configure...)
}

func newTestAgentWithPrefixes(t *testing.T, p platform.PlatformAdapter, client llm.Client, modes map[string]config.ModelSelection, provider config.ProviderConfig, store storage.Store, prefixes []string, configure ...func(*testAgentOptions)) *Agent {
	t.Helper()
	return mustNewWithOptions(t, testAgentOptions{
		Platform: p, Models: newTestModels(t, modelmgr.Options{Clients: map[string]llm.Client{"default": client}, Providers: map[string]config.ProviderConfig{"default": provider}, ModeModels: modes, DefaultMode: storage.SessionModeWork}),
		Providers: map[string]config.ProviderConfig{"default": provider}, Store: store, CommandPrefixes: prefixes,
		SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork},
	}, configure...)
}

func newTestMediaAgent(t *testing.T, p platform.PlatformAdapter, client llm.Client, store storage.Store, center *media.Manager, configure ...func(*testAgentOptions)) *Agent {
	t.Helper()
	return mustNewWithOptions(t, testAgentOptions{
		Platform: p, Store: store, Media: center, CommandPrefixes: []string{"/"},
		Models: newTestModels(t, modelmgr.Options{
			Clients:   map[string]llm.Client{"default": client},
			Providers: map[string]config.ProviderConfig{"default": {}},
			ModeModels: map[string]config.ModelSelection{
				storage.SessionModeWork: {Provider: "default", Model: "test-model"},
				storage.SessionModeChat: {Provider: "default", Model: "test-model"},
			},
			DefaultMode: storage.SessionModeWork,
		}),
		SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork},
	}, configure...)
}

// Tests without tools still exercise the same command registration path.
type testToolRegistry struct{ registry *tool.Registry }

func (r testToolRegistry) List() []tool.Info {
	if r.registry == nil {
		return nil
	}
	return r.registry.List()
}
func (r testToolRegistry) Unregister(name string) error {
	if r.registry == nil {
		return nil
	}
	return r.registry.Unregister(name)
}

func completeTest(service *completion.Service, text string) []string {
	return completion.Texts(service.Complete(context.Background(), completion.Request{Text: text}))
}

func listTestFileRollbacks(a *Agent, ctx context.Context) ([]fileops.RollbackInfo, error) {
	ctx, err := a.PrepareFileCommand(ctx, false)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a.fileCommands.service.List(ctx)
}

func rollbackTestFile(a *Agent, ctx context.Context, id uint64) (fileops.RollbackResult, error) {
	ctx, err := a.PrepareFileCommand(ctx, true)
	if err != nil {
		return fileops.RollbackResult{}, err
	}
	return a.fileCommands.service.RollbackByID(ctx, id)
}

// Test fault injection must replace the shared service at every consumer.
func setTestToolState(a *Agent, state *toolrun.StateService) {
	a.message.input.toolState = state
	a.background.toolState = state
	testToolDeps(a).state = state
	a.execution.dialogue.Preparer.Tools.State = state
	a.execution.dialogue.Preparer.Tools.State = state
}

// Test assembly preserves fault-injection conveniences without a legacy constructor.
type testAssembly struct {
	Config
	Dependencies
	RuntimeContext context.Context
}

func testChatRoute(a *Agent) *chatroute.Loop {
	loop, err := a.execution.dialogue.Routes.LoopFor(a.execution.models.ResolveMode(storage.SessionModeWork).Provider)
	if err != nil {
		panic(err)
	}
	return loop.(*chatroute.Loop)
}
func testToolDeps(a *Agent) *toolRunDeps {
	return a.execution.dialogue.Preparer.Tools.Deps.(*toolRunDeps)
}
