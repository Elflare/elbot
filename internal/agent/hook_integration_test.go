package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"elbot/internal/agent"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
)

type integrationPlatform struct {
	mu      sync.Mutex
	outputs []string
}

func (*integrationPlatform) PrimaryName() string { return "cli" }
func (p *integrationPlatform) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputs = append(p.outputs, delivery.FallbackOutput(outputs).Text)
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}
func (p *integrationPlatform) SendTo(ctx context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return p.SendChat(ctx, outputs)
}
func (*integrationPlatform) StartStream(context.Context) (delivery.MessageStream, error) {
	return nil, nil
}
func (*integrationPlatform) StartReasoning(context.Context) delivery.ReasoningStream { return nil }
func (*integrationPlatform) CallPlatformAPI(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}

type integrationClient struct{}

func (integrationClient) ListModels(context.Context) ([]string, error) { return []string{"m"}, nil }
func (integrationClient) GenerateText(context.Context, llm.TextRequest) (llm.TextResult, error) {
	return llm.TextResult{Text: "title"}, nil
}
func (integrationClient) Stream(context.Context, chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	ch := make(chan chatcompletions.Chunk, 1)
	ch <- chatcompletions.Chunk{DeltaContent: "answer"}
	close(ch)
	return ch, nil
}

func TestAgentRegistersHookCommandsAndRunsPublicBridge(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{ConfigPath: filepath.Join(root, "config.toml")}
	cfg.Session = config.SessionConfig{DefaultMode: "work", Naming: config.SessionNamingConfig{TriggerStep: 100}}
	cfg.Commands.Prefixes = []string{"/"}
	cfg.View.SessionListPageSize = 10
	cfg.Tools.MaxRoundsPerTurn = 2
	cfg.Sandbox.Root = root
	dir := config.PluginConfigDir(cfg.ConfigPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	rule := "[[rules]]\nname = 'integration_suffix'\non = 'agent.output.prepared'\nalways = true\naction = 'append'\nfield = 'message.text'\ntext = ' [hook]'\n"
	if err := os.WriteFile(filepath.Join(dir, "hooks.toml"), []byte(rule), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	models, err := modelmgr.New(modelmgr.Options{Clients: map[string]llm.Client{"p": integrationClient{}}, Providers: map[string]config.ProviderConfig{"p": {Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, DefaultMode: "work"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.Close(context.Background()) })
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	permission := security.DefaultPermission()
	tools, err := tool.NewSupervisor(t.Context(), cfg, tool.SupervisorDeps{Store: store, Models: models, Media: center, Permission: permission})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tools.Close(context.Background()) })
	platform := &integrationPlatform{}
	notifications, err := events.NotificationRequested.Connect(func(ctx context.Context, notice events.Notification) (events.NotificationReceipt, error) {
		_, _, err := platform.SendChat(ctx, []delivery.Output{delivery.Text(notice.Text)})
		return events.NotificationReceipt{}, err
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = notifications.Close(context.Background()) })
	hooks, err := hook.NewSupervisor(t.Context(), cfg, hook.SupervisorDeps{Tools: tools, Platform: platform})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hooks.Close(context.Background()) })
	a, err := agent.NewSupervisor(t.Context(), cfg, agent.SupervisorDeps{Store: store, Models: models, Media: center, Permission: permission, Tools: tools, Hooks: hooks, Platform: platform})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	if err := hooks.ConnectAgent(a); err != nil {
		t.Fatal(err)
	}
	if err := tools.Start(); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := hooks.Start(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, info := range a.Commands() {
		if info.Name == "hooks" {
			found = true
		}
	}
	if !found {
		t.Fatal("Hook command missing from Agent catalog")
	}
	items, err := a.Complete(t.Context(), completion.Request{Text: "/hooks integration", Cursor: len("/hooks integration")})
	if err != nil || len(items) != 1 || items[0].Text != "integration_suffix" {
		t.Fatalf("completion=%+v err=%v", items, err)
	}
	if err := a.HandleMessage(t.Context(), "/hooks"); err != nil {
		t.Fatal(err)
	}
	platform.mu.Lock()
	output := strings.Join(platform.outputs, "\n")
	platform.mu.Unlock()
	if !strings.Contains(output, "integration_suffix") {
		t.Fatalf("command output=%q", output)
	}
	if err := a.HandleMessage(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	platform.mu.Lock()
	output = strings.Join(platform.outputs, "\n")
	platform.mu.Unlock()
	if !strings.Contains(output, "answer [hook]") {
		t.Fatalf("public Hook bridge did not transform output: %q", output)
	}
	// A new root-built candidate must replace the loaded rule while retaining
	// the active pipeline's Agent callback and source logging.
	var logMu sync.Mutex
	var triggered []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "hook_triggered" {
			logMu.Lock()
			triggered = append(triggered, record)
			logMu.Unlock()
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	if err := os.WriteFile(filepath.Join(dir, "hooks.toml"), []byte(strings.ReplaceAll(rule, " [hook]", " [reloaded]")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.HandleMessage(t.Context(), "/hooks reload"); err != nil {
		t.Fatal(err)
	}
	if err := a.HandleMessage(t.Context(), "hello again"); err != nil {
		t.Fatal(err)
	}
	platform.mu.Lock()
	output = strings.Join(platform.outputs, "\n")
	platform.mu.Unlock()
	if !strings.Contains(output, "answer [reloaded]") {
		t.Fatalf("reloaded output=%q", output)
	}
	logMu.Lock()
	defer logMu.Unlock()
	logged := false
	for _, record := range triggered {
		if strings.Contains(record.Content, "hook=integration_suffix") && strings.Contains(record.Content, "after_text=answer [reloaded]") {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("replacement lost source logging: %+v", triggered)
	}

}
