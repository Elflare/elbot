package agent

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/command"
	commandbuiltin "elbot/internal/command/builtin"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/fileops"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/notification"
	notificationrules "elbot/internal/notification/rules"
	"elbot/internal/platform"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

// Test configuration is intentionally separate from the production constructor:
// the application supplies already assembled shared services.
type testAgentOptions struct {
	Platform        platform.PlatformAdapter
	Models          *modelmgr.Service
	Store           storage.Store
	Providers       map[string]config.ProviderConfig
	CommandPrefixes []string
	SessionConfig   session.Config
	SecurityPolicy  *security.Policy
	SandboxRoot     string
	ToolsConfig     config.ToolsConfig
	FileRollback    *fileops.Service
	ToolRegistry    *tool.Registry
}

func assembleTestOptions(opts testAgentOptions) Options {
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
	dispatcher := dispatch.New(dispatch.Options{Primary: opts.Platform, Store: opts.Store, MediaRetentionDays: defaults.Maintenance.SandboxCleanup.RetentionDays})
	notices := notification.New(dispatcher, nil, false)
	opts.Models.SetRetryNotifier(notificationrules.ModelRetry(notices))
	return Options{
		Platform: opts.Platform, Models: opts.Models, Store: opts.Store,
		Sessions: session.NewServiceWithConfig(opts.Store, opts.SessionConfig, session.NewTitleGenerator(opts.Models), nil),
		Commands: command.NewRouter(opts.CommandPrefixes), Requests: request.NewManager(0), Turns: turn.NewManager(),
		Contexts:  contextmgr.New(contextmgr.Options{Store: opts.Store, Models: opts.Models, Config: defaults.Context, Metadata: defaults.ModelMetadata, Providers: opts.Providers}),
		ToolState: toolrun.NewStateService(opts.Store), ToolRunner: toolrun.NewManager(opts.ToolRegistry, opts.SecurityPolicy),
		ToolPreloader: toolrun.NewPreloadService(toolrun.PreloadOptions{Registry: opts.ToolRegistry}),
		ToolRegistry:  opts.ToolRegistry, FileRollback: opts.FileRollback,
		Dispatcher: dispatcher, Notifications: notices, SecurityPolicy: opts.SecurityPolicy,
		SandboxRoot: opts.SandboxRoot, ToolsConfig: opts.ToolsConfig,
		LLMRequestConfig: defaults.LLMRequest, SessionIdleExpiration: defaults.Session.IdleExpiration,
	}
}

func mustNewWithOptions(t *testing.T, cfg testAgentOptions) *Agent {
	t.Helper()
	a, err := NewWithOptions(assembleTestOptions(cfg))
	if err != nil {
		t.Fatal(err)
	}
	a.sessions.SetForegroundActivation(a.AdoptForeground)
	a.sessions.SetActivitySource(func() []string {
		var ids []string
		for _, active := range a.turns.SnapshotAll() {
			if active.Phase != turn.PhaseIdle {
				ids = append(ids, active.SessionID)
			}
		}
		return ids
	})
	if err := commandbuiltin.RegisterDefaultModules(a.commands, commandbuiltin.Deps{
		Router: a.commands, Sessions: a.sessions, Requests: a.requests, Turns: a.turns, Store: a.store,
		Scope: a.Scope, Models: a.models, Contexts: a.contexts, Compact: a,
		Tools: testToolRegistry{a}, FileRollback: a.toolRuntime.fileRollback, PrepareFileContext: a.PrepareFileCommand,
		SessionState: commandbuiltin.NewSessionCommandState(config.Default().View.SessionListPageSize, 30),
		Audit:        a.audit, RuntimeStatus: a.RuntimeStatus,
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

func newTestAgent(t *testing.T, p platform.PlatformAdapter, client llm.LLM, model string, provider config.ProviderConfig, store storage.Store) *Agent {
	t.Helper()
	return newTestAgentWithPrefixes(t, p, client, map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "default", Model: model},
		storage.SessionModeChat: {Provider: "default", Model: model},
	}, provider, store, []string{"/"})
}

func newTestAgentWithPrefixes(t *testing.T, p platform.PlatformAdapter, client llm.LLM, modes map[string]config.ModelSelection, provider config.ProviderConfig, store storage.Store, prefixes []string) *Agent {
	t.Helper()
	return mustNewWithOptions(t, testAgentOptions{
		Platform: p, Models: newTestModels(t, modelmgr.Options{Clients: map[string]llm.LLM{"default": client}, Providers: map[string]config.ProviderConfig{"default": provider}, ModeModels: modes, DefaultMode: storage.SessionModeWork}),
		Providers: map[string]config.ProviderConfig{"default": provider}, Store: store, CommandPrefixes: prefixes,
		SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork},
	})
}

// Some execution tests replace the registry after creating the Agent. This
// test-only adapter keeps their command view attached to that registry.
type testToolRegistry struct{ a *Agent }

func (r testToolRegistry) List() []tool.Info {
	if r.a.toolRuntime.registry == nil {
		return nil
	}
	return r.a.toolRuntime.registry.List()
}
func (r testToolRegistry) Unregister(name string) error {
	if r.a.toolRuntime.registry == nil {
		return nil
	}
	return r.a.toolRuntime.registry.Unregister(name)
}

func (a *Agent) SetToolRuntime(registry *tool.Registry, _ any) {
	a.toolRuntime.registry = registry
	a.toolRuntime.preloader = toolrun.NewPreloadService(toolrun.PreloadOptions{Registry: registry, Audit: a.audit})
	a.toolRuntime.manager = toolrun.NewManager(registry, a.securityPolicy)
	a.toolRuntime.manager.Media = a.media
	if registry != nil {
		a.toolRuntime.provider = toolRunPromptProvider{agent: a}
		a.toolRuntime.defaultProvider = true
	}
	a.rebuildSystemPrompt()
}

func listTestFileRollbacks(a *Agent, ctx context.Context) ([]fileops.RollbackInfo, error) {
	ctx, err := a.PrepareFileCommand(ctx, false)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a.toolRuntime.fileRollback.List(ctx)
}

func rollbackTestFile(a *Agent, ctx context.Context, id uint64) (fileops.RollbackResult, error) {
	ctx, err := a.PrepareFileCommand(ctx, true)
	if err != nil {
		return fileops.RollbackResult{}, err
	}
	return a.toolRuntime.fileRollback.RollbackByID(ctx, id)
}
func (a *Agent) SetToolTagConfig(path string, cfg config.ToolTagsConfig) {
	a.toolRuntime.preloader = toolrun.NewPreloadService(toolrun.PreloadOptions{Registry: a.toolRuntime.registry, TagsPath: path, Tags: cfg, Audit: a.audit})
	a.rebuildSystemPrompt()
}

func (a *Agent) setTestHookManager(manager hook.Manager) {
	if manager == nil {
		manager = hook.NoopManager{}
	}
	if concrete, ok := manager.(*hook.DefaultManager); ok {
		concrete.SetWakeupFunc(a.HookWakeup)
		concrete.SetObserver(a.ObserveHookRun)
	}
	a.hooks = manager
}
