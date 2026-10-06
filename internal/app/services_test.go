package app

import (
	"context"
	"elbot/internal/llm/chatcompletions"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/background"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	elcron "elbot/internal/cron"
	"elbot/internal/delivery"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type assemblyPlatform struct {
	mu        sync.Mutex
	outputs   []string
	catalog   []command.Info
	completer *completion.Service
}

func (*assemblyPlatform) Name() string                                        { return "cli" }
func (*assemblyPlatform) Run(context.Context, platform.PlatformHandler) error { return nil }
func (p *assemblyPlatform) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputs = append(p.outputs, delivery.FallbackOutput(outputs).Text)
	return delivery.Receipt{PlatformMessageIDs: []string{fmt.Sprint(len(p.outputs))}}, nil
}
func (p *assemblyPlatform) SendNotice(ctx context.Context, notice delivery.Notice) (delivery.Receipt, error) {
	return p.SendChat(ctx, notice.Outputs)
}
func (p *assemblyPlatform) SetCommandCatalog(infos []command.Info)   { p.catalog = infos }
func (p *assemblyPlatform) SetCompleter(service *completion.Service) { p.completer = service }
func (p *assemblyPlatform) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.outputs, "\n")
}

type assemblyModel struct {
	mu     sync.Mutex
	chunks [][]chatcompletions.Chunk
	models []string
}

func (*assemblyModel) ListModels(context.Context) ([]string, error) {
	return []string{"first", "second"}, nil
}
func (m *assemblyModel) Stream(_ context.Context, req chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.models = append(m.models, req.Model)
	chunks := []chatcompletions.Chunk{{DeltaContent: "assembled answer"}}
	if len(m.chunks) > 0 {
		chunks, m.chunks = m.chunks[0], m.chunks[1:]
	}
	ch := make(chan chatcompletions.Chunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}
func (m *assemblyModel) selections() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.models...)
}

func runtimeAssemblyFixture(t *testing.T) (RuntimeRequest, *assemblyPlatform, *assemblyModel) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Default()
	cfg.ConfigPath, cfg.StateConfigPath = filepath.Join(root, "config.toml"), filepath.Join(root, "state.toml")
	cfg.Sandbox.Root, cfg.Soul.Path = filepath.Join(root, "sandbox"), ""
	cfg.ToolTagsConfigPath = filepath.Join(root, "tool_tags.toml")
	cfg.Storage.SessionsSQLitePath = filepath.Join(root, "sessions.db")
	cfg.Providers = map[string]config.ProviderConfig{"test": {Models: []string{"first", "second"}}}
	cfg.ModeModels = map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "test", Model: "first"},
		storage.SessionModeChat: {Provider: "test", Model: "first"},
	}
	cfg.Session.DefaultMode, cfg.Session.Naming.TriggerStep = storage.SessionModeWork, 100
	cfg.Security.SuperadminConfirmRisk = "critical"
	cfg.Security.Superadmins = map[string][]string{"cli": {"local"}}
	cfg.FileDelivery.Backend = "base64"
	cfg.Elnis.Enabled = false
	if err := os.WriteFile(cfg.ConfigPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.New(context.Background(), cfg.Storage.SessionsSQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	logs, err := logging.NewManager("error", cfg.Storage.SessionsSQLitePath, 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Close() })
	p, model := &assemblyPlatform{}, &assemblyModel{}
	events := []string{}
	return RuntimeRequest{
		Foundation: &FoundationComponents{Config: cfg, Store: store, Logs: logs, Logger: logs.Runtime(), CronManager: elcron.NewManager(store.CronJobs(), logs.Runtime())},
		Models:     ModelClients{ByProvider: map[string]llm.Client{"test": model}},
		Platforms:  PlatformComponents{Primary: p, Runtimes: []platform.Runtime{p}},
		Profiler:   profilerStub{events: &events},
	}, p, model
}

func closeAssembledRuntime(t *testing.T, runtime *RuntimeComponents) {
	t.Helper()
	if runtime == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if runtime.Signals != nil {
		if err := runtime.Signals.Close(ctx); err != nil {
			t.Error(err)
		}
	}
	if runtime.Lifecycle != nil {
		if err := runtime.Lifecycle.Close(ctx); err != nil {
			t.Error(err)
		}
	}
}

func TestRuntimeAssemblyCommandsShareToolAndModelServices(t *testing.T) {
	req, p, model := runtimeAssemblyFixture(t)
	path := filepath.Join(filepath.Dir(req.Foundation.Config.ConfigPath), "edited.txt")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "expected_revision": fileops.ContentRevision([]byte("before")), "edits": []map[string]any{{"operation": "overwrite", "new_text": "after"}}})
	model.chunks = [][]chatcompletions.Chunk{{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "edit", Name: "edit_file", Args: string(args)}}, FinishReason: "tool_calls"}}}
	runtime, err := (defaultRuntimeFactory{}).Build(context.Background(), req)
	t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (defaultIntegrationFactory{}).Attach(context.Background(), IntegrationRequest{Foundation: req.Foundation, Runtime: runtime, Platforms: req.Platforms, Mode: RunModeCLIOnly, Profiler: req.Profiler}); err != nil {
		t.Fatal(err)
	}
	if p.completer == nil || len(p.catalog) != len(runtime.Commands.Commands()) {
		t.Fatal("platform did not receive the registered commands and completion service")
	}
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin})
	send := func(text string) {
		t.Helper()
		if err := runtime.Handler.HandleMessage(ctx, text); err != nil {
			t.Fatal(err)
		}
	}
	send("edit the file")
	if data, _ := os.ReadFile(path); string(data) != "after" {
		t.Fatalf("tool edit = %q; output: %s", data, p.text())
	}
	send("/rollback")
	handler, _ := runtime.Commands.Handler("rollback")
	choices := handler.(command.Completer).Complete(ctx, command.CompletionRequest{Raw: "/rollback ", Prefix: "/", Name: "rollback", Cursor: len("/rollback ")})
	if len(choices) != 1 {
		t.Fatalf("shared backup completion = %#v; output: %s", choices, p.text())
	}
	calls := len(model.selections())
	send("/rollback " + choices[0].Text)
	if data, _ := os.ReadFile(path); string(data) != "before" {
		t.Fatalf("command rollback = %q; output: %s", data, p.text())
	}
	if len(model.selections()) != calls {
		t.Fatal("rollback unexpectedly used the model")
	}
	if !strings.Contains(p.text(), "已恢复编辑前内容") {
		t.Fatal("missing rollback notice")
	}
	oldContext, err := runtime.Agent.PrepareFileCommand(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	send("/model --work test/second")
	if got := runtime.Models.CurrentModelForMode("work"); got.Model != "second" {
		t.Fatalf("shared model = %#v; output: %s", got, p.text())
	}
	state, err := os.ReadFile(req.Foundation.Config.StateConfigPath)
	if err != nil || !strings.Contains(string(state), "second") {
		t.Fatalf("model persistence = %q, %v", state, err)
	}
	send("/new")
	send("next session")
	if _, err := runtime.Agent.PrepareFileCommand(oldContext, false); err == nil {
		t.Fatal("old file binding was revived")
	}
	selections := model.selections()
	if selections[len(selections)-1] != "second" {
		t.Fatalf("next request used %v", selections)
	}
	before := p.text()
	result, err := runtime.Agent.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "assembly", Platform: "cli", Actor: contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin}, Prompt: "background", ModelProvider: "test", Model: "second"})
	if err != nil || result.Text != "assembled answer" || result.SessionID == "" {
		t.Fatalf("background = %#v, %v", result, err)
	}
	if p.text() != before {
		t.Fatal("background task leaked foreground output")
	}
}

func TestRuntimeAssemblyFailureReturnsOwnedWorkers(t *testing.T) {
	req, _, _ := runtimeAssemblyFixture(t)
	req.Foundation.Config.Tools.MaxRoundsPerTurn = -1 // Agent validation after Hook and Skill workers exist.
	runtime, err := (defaultRuntimeFactory{}).Build(context.Background(), req)
	if err == nil || runtime == nil || runtime.Lifecycle == nil {
		t.Fatalf("partial runtime = %v, %v", runtime, err)
	}
	lifecycle := runtime.Lifecycle.(*runtimeLifecycle)
	if lifecycle.hooks == nil || lifecycle.skillDone == nil {
		t.Fatal("test did not reach worker creation")
	}
	closeAssembledRuntime(t, runtime)
	if !lifecycle.stopped() {
		t.Fatal("startup workers survived cleanup")
	}
}

func (m *assemblyModel) GenerateText(ctx context.Context, req llm.TextRequest) (llm.TextResult, error) {
	messages := []llm.LLMMessage{}
	if req.Instructions != "" {
		messages = append(messages, llm.LLMMessage{Role: llm.RoleSystem, Segments: llm.TextSegments(req.Instructions)})
	}
	messages = append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments(req.Input)})
	chunks, err := m.Stream(ctx, chatcompletions.Request{Model: req.Model, Messages: messages, MaxTokens: req.MaxOutputTokens, ExtraBody: req.ExtraBody})
	if err != nil {
		return llm.TextResult{}, err
	}
	result := llm.TextResult{}
	for chunk := range chunks {
		if chunk.Error != nil {
			return llm.TextResult{}, chunk.Error
		}
		result.Text += chunk.DeltaContent
		if chunk.Usage != nil {
			result.Usage = chunk.Usage
		}
	}
	return result, ctx.Err()
}
