package module

import (
	"context"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

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
func isolatedModuleDeps(t *testing.T) ModuleDeps {
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
	t.Cleanup(func() { _ = models.Close() })
	registry := tool.NewRegistry()
	policy := security.DefaultPolicy()
	sender := dispatch.New(dispatch.Options{Primary: &assemblyPlatform{}, Store: store})
	return ModuleDeps{PlatformName: "cli", Store: store, Models: models, Policy: policy, Dispatcher: sender, Registry: registry, ToolManager: toolrun.NewManager(registry, policy), Preloader: toolrun.NewPreloadService(toolrun.PreloadOptions{Registry: registry}), ToolState: toolrun.NewStateService(store), Files: fileops.NewService(nil), Session: config.SessionConfig{DefaultMode: "work", Naming: config.SessionNamingConfig{TriggerStep: 100}}, MaxRounds: 2, Prefixes: []string{"/"}, PageSize: 10}
}
func TestStandaloneDialogueOwnsStateAndBorrowsOnlyCapabilities(t *testing.T) {
	deps := isolatedModuleDeps(t)
	m, err := NewModule(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer m.Close(ctx)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.Handler().HandleMessage(t.Context(), "hello"); err != nil {
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
	case <-m.Sessions().Done():
	default:
		t.Fatal("naming owner did not stop")
	}
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
	deps.MaxRounds = 0
	m, err := NewModule(t.Context(), deps)
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
	if err := deps.Store.Media().RecoverInterrupted(ctx); err != nil {
		t.Fatal("borrowed storage closed", err)
	}
}
