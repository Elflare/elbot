package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/processenv"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool/discovery"
	"elbot/internal/tool/execution"
	toolmedia "elbot/internal/tool/media"
	"elbot/internal/tool/preload"
	toolregistry "elbot/internal/tool/registry"
	"elbot/internal/tool/skill"
	toolstate "elbot/internal/tool/state"
)

type isolatedToolCapabilities struct {
	*toolregistry.Registry
	*execution.Manager
	*preload.PreloadService
	*toolstate.StateService
	*resident.Store
	*fileops.Service
	skills *skill.Manager
}

type isolatedHookCapabilities struct{}

func (isolatedHookCapabilities) Commands() []command.Handler { return nil }

type isolatedPlatformCapabilities struct{ *dispatch.Router }

func (isolatedPlatformCapabilities) CallPlatformAPI(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}
func (isolatedPlatformCapabilities) PrimaryName() string { return "cli" }
func (isolatedPlatformCapabilities) StartReasoning(context.Context) delivery.ReasoningStream {
	return nil
}

func isolatedAgentConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Session = config.SessionConfig{DefaultMode: "work", Naming: config.SessionNamingConfig{TriggerStep: 100}}
	cfg.Tools.MaxRoundsPerTurn = 2
	cfg.Commands.Prefixes = []string{"/"}
	cfg.View.SessionListPageSize = 10
	return cfg
}

type isolatedClient struct{}

func (isolatedClient) ListModels(context.Context) ([]string, error) { return []string{"m"}, nil }

func (isolatedClient) GenerateText(context.Context, llm.TextRequest) (llm.TextResult, error) {
	return llm.TextResult{Text: "title"}, nil
}

func (isolatedClient) Stream(context.Context, chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	ch := make(chan chatcompletions.Chunk, 1)
	ch <- chatcompletions.Chunk{DeltaContent: "isolated answer"}
	close(ch)
	return ch, nil
}

func isolatedModuleDeps(t *testing.T) SupervisorDeps {
	t.Helper()
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	models, err := modelmgr.New(modelmgr.Options{Clients: map[string]llm.Client{"p": isolatedClient{}}, Providers: map[string]config.ProviderConfig{"p": {Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, DefaultMode: "work"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.Close(context.Background()) })
	permission := security.DefaultPermission()
	registry := toolregistry.NewRegistry(permission)
	sender := dispatch.New(dispatch.Options{Primary: &assemblyPlatform{}, Store: store})
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	return SupervisorDeps{
		Media: center, Store: store, Models: models, Permission: permission,
		Platform: isolatedPlatformCapabilities{sender},
		Hooks:    isolatedHookCapabilities{},
		Tools: isolatedToolCapabilities{
			Registry: registry, Manager: execution.NewManager(execution.ManagerDeps{Registry: registry, Permission: permission, Media: toolmedia.NewMediaRuntime(center, root), InfoAvailableInContext: toolregistry.InfoAvailableInContext, UnavailableReason: toolregistry.UnavailableReason, BackgroundDiscoveredTools: toolregistry.BackgroundDiscoveredTools, DecodeState: toolstate.DecodeState, PrepareUpdate: toolstate.PrepareUpdate, DiscoveryStateUpdate: discovery.NewResolver(registry).DiscoveryStateUpdate, Log: func(context.Context, events.LogRecord) {}}),
			PreloadService: preload.NewPreloadService(preload.PreloadOptions{Registry: registry, Resolver: discovery.NewResolver(registry), NativeDiscoveredToolsFromDiscovery: discovery.NativeDiscoveredToolsFromDiscovery, LoadDiscoveryContent: discovery.LoadDiscoveryContent, BackgroundDiscoveredTools: toolregistry.BackgroundDiscoveredTools}),
			StateService:   toolstate.NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools),
			Store:          resident.NewStore(filepath.Join(t.TempDir(), "memory.toml")),
			Service:        fileops.NewService(nil),
			skills:         skill.NewManager(t.TempDir(), registry, toolmedia.NewMediaRuntime(center, root), processenv.New(nil)),
		},
	}
}

func TestStandaloneDialogueOwnsStateAndBorrowsOnlyCapabilities(t *testing.T) {
	deps := isolatedModuleDeps(t)
	m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer m.Close(ctx)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.HandleMessage(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	rows, err := deps.Store.Sessions().List(t.Context(), storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	messages, err := deps.Store.Messages().ListBySession(t.Context(), rows[0].ID)
	if err != nil || len(messages) != 2 || messages[1].Content != "isolated answer" {
		t.Fatal(messages, err)
	}
	if len(m.requests.ListBySession(rows[0].ID)) != 0 {
		t.Fatal("request registration leaked")
	}
	for range 2 {
		if err := m.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-m.Done():
	default:
		t.Fatal("naming owner did not stop")
	}
	assertAgentSignalsClosed(t, m)
	if _, err := deps.Store.Messages().ListBySession(ctx, rows[0].ID); err != nil {
		t.Fatal("borrowed Store closed", err)
	}
	if deps.Models.ClientForProvider("p") == nil {
		t.Fatal("borrowed models changed")
	}
	if err := m.Start(); err == nil {
		t.Fatal("closed module restarted")
	}
}

func TestDialoguePartialConstructionCleansOwnedState(t *testing.T) {
	deps := isolatedModuleDeps(t)
	if err := deps.Models.ModelRetrying().Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
	if err == nil || m == nil || m.sessions == nil {
		t.Fatal(m, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.Done():
	default:
		t.Fatal("partial state remained alive")
	}
	assertAgentSignalsClosed(t, m)
	if err := deps.Store.Media().RecoverInterrupted(ctx); err != nil {
		t.Fatal("borrowed storage closed", err)
	}
}
