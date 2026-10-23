package execution

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	chatroute "elbot/internal/agent/chat"
	"elbot/internal/agent/dialogue"
	agenthooks "elbot/internal/agent/hooks"
	"elbot/internal/agent/routes"

	"elbot/internal/command"
	commandbuiltin "elbot/internal/command/builtin"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	notificationrules "elbot/internal/notification/rules"
	"elbot/internal/platform"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
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
	HookRuntime           agenthooks.HookRouter
	SoulPath              string
	ResidentMemoryStore   *resident.Store
}

func assembleTestOptions(t *testing.T, opts testAgentOptions) testAssembly {
	defaults := config.Default()
	if opts.ToolRegistry == nil {
		opts.ToolRegistry = tool.NewRegistry()
	}
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
	if opts.Media == nil {
		root := t.TempDir()
		opts.Media = media.NewManager(opts.Store, root, &media.LocalBackend{Root: root})
	}
	if opts.ResidentMemoryStore == nil {
		opts.ResidentMemoryStore = resident.NewStore(filepath.Join(t.TempDir(), "memory.toml"))
	}
	if opts.FileRollback == nil {
		opts.FileRollback = fileops.NewService(nil)
	}
	if opts.SessionConfig.DefaultMode == "" {
		opts.SessionConfig.DefaultMode = defaults.Session.DefaultMode
	}
	if opts.SessionConfig.TriggerStep <= 0 {
		opts.SessionConfig.TriggerStep = defaults.Session.Naming.TriggerStep
	}
	dispatcher := dispatch.New(dispatch.Options{Primary: opts.Platform, Store: opts.Store, Media: opts.Media, MediaRetentionDays: defaults.Maintenance.SandboxCleanup.RetentionDays})
	connectFixtureNotifications(t, opts.Platform.Name(), dispatcher)
	_, _ = opts.Models.ModelRetrying().Observe(func(ctx context.Context, event modelmgr.ModelRetryingEvent) error {
		text := notificationrules.ModelRetry(event.Provider, event.Retry)
		if text == "" {
			return nil
		}
		_, err := events.NotificationRequested.Emit(ctx, events.NewNotification(ctx, slog.LevelWarn, text))
		return err
	}, signal.ConnectOptions{})
	registry := routes.New()
	optsResult := testAssembly{RuntimeContext: opts.RuntimeContext,
		SoulPath: opts.SoulPath, LLMRequestConfig: defaults.LLMRequest, SessionIdleExpiration: defaults.Session.IdleExpiration, SandboxRoot: opts.SandboxRoot, ToolsConfig: opts.ToolsConfig,

		Routes: registry, Platform: opts.Platform, Models: opts.Models, Store: opts.Store, Media: opts.Media,
		Sessions: session.NewServiceWithConfig(opts.Store, opts.SessionConfig, session.NewTitleGenerator(opts.Models)),
		Commands: command.NewRouter(opts.CommandPrefixes), Requests: request.NewManager(0), Turns: turn.NewManager(),
		Contexts:  contextmgr.New(contextmgr.Options{Compactors: registry, Store: opts.Store, Models: opts.Models, Config: defaults.Context}),
		ToolState: toolrun.NewStateService(opts.Store), ToolRunner: toolrun.NewManager(opts.ToolRegistry, opts.SecurityPolicy),
		ToolPreloader: toolrun.NewPreloadService(toolrun.PreloadOptions{Registry: opts.ToolRegistry, TagsPath: opts.ToolTagsPath, Tags: opts.ToolTags}),
		ToolRegistry:  opts.ToolRegistry, FileRollback: opts.FileRollback, Dispatcher: dispatcher, SecurityPolicy: opts.SecurityPolicy,
		ToolProvider: opts.ToolProvider, HookManager: opts.HookManager, HookRuntime: opts.HookRuntime, ResidentMemoryStore: opts.ResidentMemoryStore,
	}

	optsResult.ToolRunner.Media = opts.Media
	if opts.SessionIdleExpiration != nil {
		optsResult.SessionIdleExpiration = *opts.SessionIdleExpiration
	}
	return optsResult
}

func mustNewWithOptions(t *testing.T, cfg testAgentOptions, configure ...func(*testAgentOptions)) *agentFixture {
	t.Helper()
	for _, change := range configure {
		change(&cfg)
	}
	opts := assembleTestOptions(t, cfg)
	runtimeCtx := opts.RuntimeContext
	if runtimeCtx == nil {
		runtimeCtx = t.Context()
	}
	a, err := newTestAssembly(runtimeCtx, opts)
	if err != nil {
		t.Fatal(err)
	}
	display, _ := opts.Platform.(interface {
		SetRuntimeStatus(context.Context, runtimestatus.Snapshot) error
	})
	connectTestObservers(a, assembleObserverOptions{display: display})
	connectTestForeground(t, opts.Sessions, a.AdoptForeground)
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
		SessionState:  commandbuiltin.NewSessionCommandState(config.Default().View.SessionListPageSize, 30),
		RuntimeStatus: a.RuntimeStatus,
		Usage:         a.status.Usage,
	}); err != nil {
		t.Fatal(err)
	}
	if manager, ok := opts.HookManager.(*hook.DefaultManager); ok {
		manager.SetWakeupFunc(a.HookWakeup)
		manager.SetObserver(a.ObserveHookRun)
	}
	return a
}

func newTestAgent(t *testing.T, p platform.PlatformAdapter, client llm.Client, model string, provider config.ProviderConfig, store storage.Store, configure ...func(*testAgentOptions)) *agentFixture {
	t.Helper()
	return newTestAgentWithPrefixes(t, p, client, map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "default", Model: model},
		storage.SessionModeChat: {Provider: "default", Model: model},
	}, provider, store, []string{"/"}, configure...)
}

func newTestAgentWithPrefixes(t *testing.T, p platform.PlatformAdapter, client llm.Client, modes map[string]config.ModelSelection, provider config.ProviderConfig, store storage.Store, prefixes []string, configure ...func(*testAgentOptions)) *agentFixture {
	t.Helper()
	root := t.TempDir()
	return mustNewWithOptions(t, testAgentOptions{
		Platform: p, Models: newTestModels(t, modelmgr.Options{
			Clients:     map[string]llm.Client{"default": client},
			Providers:   map[string]config.ProviderConfig{"default": provider},
			ModeModels:  modes,
			DefaultMode: storage.SessionModeWork,
		}),
		Providers: map[string]config.ProviderConfig{"default": provider}, Store: store, CommandPrefixes: prefixes, Media: media.NewManager(store, root, &media.LocalBackend{Root: root}),
		SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork},
	}, configure...)
}

func newTestMediaAgent(t *testing.T, p platform.PlatformAdapter, client llm.Client, store storage.Store, center *media.Manager, configure ...func(*testAgentOptions)) *agentFixture {
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
	items, _ := service.Complete(context.Background(), completion.Request{Text: text})
	return completion.Texts(items)
}

func listTestFileRollbacks(a *agentFixture, ctx context.Context) ([]fileops.RollbackInfo, error) {
	ctx, err := a.PrepareFileCommand(ctx, false)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a.files.List(ctx)
}

func rollbackTestFile(a *agentFixture, ctx context.Context, id uint64) (fileops.RollbackResult, error) {
	ctx, err := a.PrepareFileCommand(ctx, true)
	if err != nil {
		return fileops.RollbackResult{}, err
	}
	return a.files.RollbackByID(ctx, id)
}

// Test fault injection must replace the shared service at every consumer.
func setTestToolState(a *agentFixture, state *toolrun.StateService) {
	*a.toolState = *state
}

// Test assembly preserves fault-injection conveniences without a legacy constructor.
type testAssembly struct {
	Routes              *routes.Registry
	Commands            *command.Router
	Sessions            *session.Service
	Requests            *request.Manager
	Turns               *turn.Manager
	ToolRunner          *toolrun.Manager
	ToolPreloader       *toolrun.PreloadService
	Platform            platform.PlatformAdapter
	Models              *modelmgr.Service
	Contexts            *contextmgr.Service
	ToolState           *toolrun.StateService
	Store               storage.Store
	Media               *media.Manager
	ResidentMemoryStore *resident.Store
	HookManager         hook.Manager
	HookRuntime         agenthooks.HookRouter
	Dispatcher          *dispatch.Router

	ToolRegistry          *tool.Registry
	FileRollback          *fileops.Service
	ToolProvider          dialogue.ToolSchemaProvider
	SecurityPolicy        *security.Policy
	SoulPath              string
	LLMRequestConfig      config.LLMRequestConfig
	SessionIdleExpiration config.SessionIdleExpirationConfig
	SandboxRoot           string
	ToolsConfig           config.ToolsConfig
	RuntimeContext        context.Context
}

func testChatRoute(a *agentFixture) *chatroute.Loop {
	loop, err := a.dialogue.Routes.LoopFor(a.execution.models.ResolveMode(storage.SessionModeWork).Origin.APIType)
	if err != nil {
		panic(err)
	}
	return loop.(*chatroute.Loop)
}

func testToolDeps(a *agentFixture) toolrun.RunnerDeps {
	return a.dialogue.Preparer.Tools.Deps.ForTurn(nil, "")
}
