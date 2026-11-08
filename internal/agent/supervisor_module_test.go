package agent

import (
	"context"
	"elbot/internal/testutil"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/delivery"
	platformshare "elbot/internal/platform/share"

	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/platform/dispatch"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
	"elbot/internal/tool"
)

type isolatedPlatformCapabilities struct{ *dispatch.Router }

func (isolatedPlatformCapabilities) RestoreMessageContext(ctx, _ context.Context) context.Context {
	return ctx
}

func (isolatedPlatformCapabilities) SessionIDCode(name string, _ contextinfo.ChatType) (string, error) {
	return name, nil
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

func (isolatedClient) RetryOptions() llm.RetryOptions { return llm.RetryOptions{} }

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
	deps, _ := isolatedModuleResources(t)
	return deps
}

// 需要跨消费者接线或控制生命周期的测试由装配方持有具体模型对象。
func isolatedModuleResources(t *testing.T) (SupervisorDeps, *modelmgr.ModelManager) {
	t.Helper()
	cfg := isolatedAgentConfig()
	root := t.TempDir()
	cfg.ConfigPath = filepath.Join(root, "config.toml")
	cfg.Sandbox.Root = root
	return configuredModuleResources(t, cfg)
}
func configuredModuleResources(t *testing.T, cfg *config.Config) (SupervisorDeps, *modelmgr.ModelManager) {
	t.Helper()
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	history, err := sqlite.NewChatHistory(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.Close() })
	models, err := newConfiguredTestModels(t, testModelOptions{Clients: map[string]llm.Client{"p": isolatedClient{}}, Providers: map[string]config.ProviderConfig{"p": {Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, DefaultMode: "work"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.Close(context.Background()) })
	permission := security.DefaultPermission()
	controller, err := control.NewSupervisor(cfg, control.SupervisorDeps{})
	if err != nil {
		t.Fatal(err)
	}
	sender := dispatch.New(dispatch.Options{Primary: &assemblyPlatform{}})
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, t.TempDir())
	tools, err := tool.NewSupervisor(t.Context(), cfg, tool.SupervisorDeps{Store: store, ChatHistory: history.Repository(), Models: models, Media: center, Permission: permission})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tools.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := tools.Start(); err != nil {
		t.Fatal(err)
	}
	return SupervisorDeps{
		Control: controller, Media: center, Store: store, ChatHistory: history.Repository(), Models: models, Permission: permission,
		Platform: isolatedPlatformCapabilities{sender},
		Tools:    tools,
	}, models
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
	if err := m.HandleMessage(testutil.WithCLIConversation(t.Context()), MessageRequest{Text: "hello"}); err != nil {
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
	if deps.Models.ResolveMode("work").Client == nil {
		t.Fatal("borrowed models changed")
	}
	if err := m.Start(); err == nil {
		t.Fatal("closed module restarted")
	}
}

func TestAgentConstructionDoesNotSubscribeToModelRetries(t *testing.T) {
	deps, models := isolatedModuleResources(t)
	if err := models.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
	if err != nil || m == nil || m.sessions == nil {
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
		t.Fatal("Agent state remained alive")
	}
	assertAgentSignalsClosed(t, m)
	if err := deps.Store.Media().RecoverInterrupted(ctx); err != nil {
		t.Fatal("borrowed storage closed", err)
	}
}

func (p isolatedPlatformCapabilities) SendCommandResult(ctx context.Context, req command.Request, result *command.Result, failure *command.Error) (delivery.Receipt, error) {
	var outputs []delivery.Output
	if result != nil {
		out := delivery.Text(platformshare.FormatCommandResult(req, result))
		if result.Confirmation != nil {
			out.Actions = result.Confirmation.Actions
		}
		outputs = append(outputs, out)
	}
	if failure != nil {
		outputs = append(outputs, delivery.Text(failure.Message))
	}
	return p.SendTo(ctx, delivery.Target{}, outputs)
}
