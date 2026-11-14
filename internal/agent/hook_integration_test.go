package agent_test

import (
	"context"
	"elbot/internal/testutil"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/agent"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	platformshare "elbot/internal/platform/share"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
	"elbot/internal/tool"
	"fmt"
	"net/http"
	"net/http/httptest"
)

type integrationPlatform struct {
	mu      sync.Mutex
	outputs []string
}

func (*integrationPlatform) NormalizeInput(_ context.Context, segments []llm.MessageSegment) (operationresult.Result[[]llm.MessageSegment], error) {
	return operationresult.Result[[]llm.MessageSegment]{Status: operationresult.Success, Data: segments}, nil
}

func (*integrationPlatform) SessionIDCode(name string, _ contextinfo.ChatType) (string, error) {
	return name, nil
}

func (*integrationPlatform) RestoreMessageContext(ctx, _ context.Context) context.Context { return ctx }
func (p *integrationPlatform) SendTo(_ context.Context, _ delivery.Target, outputs []delivery.Output) (operationresult.Result[delivery.Receipt], error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputs = append(p.outputs, delivery.FallbackOutput(outputs).Text)
	return operationresult.Result[delivery.Receipt]{Status: operationresult.Success}, nil
}
func (p *integrationPlatform) SendCommandResult(ctx context.Context, req command.Request, result command.Result, failure error) (delivery.Receipt, error) {
	outputs, err := platformshare.CommandResultOutputs(req, result, failure)
	if err != nil {
		return delivery.Receipt{}, err
	}
	p.mu.Lock()
	if len(outputs) > 0 {
		p.outputs = append(p.outputs, delivery.FallbackOutput(outputs).Text)
	}
	p.mu.Unlock()
	return delivery.Receipt{}, nil
}
func (p *integrationPlatform) StartStream(context.Context) (operationresult.Result[delivery.MessageStream], error) {
	stream := delivery.StreamFunc(func(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
		if event.Type != delivery.StreamSend {
			return delivery.Receipt{}, nil
		}
		sent, err := p.SendTo(ctx, delivery.Target{}, []delivery.Output{{Kind: delivery.KindText, Text: event.Text, Actions: event.Actions}})
		if err != nil {
			return delivery.Receipt{}, err
		}
		if sent.Status != operationresult.Success {
			return delivery.Receipt{}, fmt.Errorf("%s", sent.Message)
		}
		return sent.Data, nil
	})
	return operationresult.Result[delivery.MessageStream]{Status: operationresult.Success, Data: stream}, nil
}
func (*integrationPlatform) CallPlatformAPI(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}

func TestHookManagementUsesControlAndBridgeRemainsActive(t *testing.T) {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}

data: [DONE]

`)
	}))
	t.Cleanup(server.Close)
	models, err := modelmgr.NewModelManager(t.Context(), &config.Config{Providers: map[string]config.ProviderConfig{"p": {BaseURL: server.URL, Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.Close(context.Background()) })
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, root)
	permission := security.DefaultPermission()
	tools, err := tool.NewSupervisor(t.Context(), cfg, tool.SupervisorDeps{Store: store, Models: models, Media: center, Permission: permission})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tools.Close(context.Background()) })
	controller, err := control.NewSupervisor(cfg, control.SupervisorDeps{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	platform := &integrationPlatform{}
	hooks, err := hook.NewSupervisor(t.Context(), cfg, hook.SupervisorDeps{Control: controller, Tools: tools, Platform: platform})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hooks.Close(context.Background()) })
	a, err := agent.NewSupervisor(t.Context(), cfg, agent.SupervisorDeps{Control: controller, Store: store, Models: models, Media: center, Permission: permission, Tools: tools, Platform: platform})
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
	commandCtx := testutil.WithCLIConversation(t.Context())
	commandCtx = contextinfo.WithActor(commandCtx, contextinfo.Actor{Role: contextinfo.RoleSuperadmin, ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}})
	commands, err := controller.ListCommands(commandCtx, control.ListCommandsRequest{Name: "hooks"})
	if err != nil || len(commands.Items) != 1 || commands.Items[0].Name != "hooks" {
		t.Fatalf("Hook command missing from control catalog: %+v err=%v", commands, err)
	}
	items, err := a.Complete(testutil.WithCLIConversation(t.Context()), completion.Request{Text: "/hooks integration", Cursor: len("/hooks integration")})
	if err != nil || len(items) != 1 || items[0].Text != "integration_suffix" {
		t.Fatalf("completion=%+v err=%v", items, err)
	}
	commandCtx = platformshare.WithMessageContext(commandCtx, platformshare.MessageContext{CommandReply: platform})
	if err := a.HandleMessage(commandCtx, agent.MessageRequest{Text: "/hooks"}); err != nil {
		t.Fatal(err)
	}
	platform.mu.Lock()
	output := strings.Join(platform.outputs, "\n")
	platform.mu.Unlock()
	if !strings.Contains(output, "integration_suffix") {
		t.Fatalf("command output=%q", output)
	}
	if err := a.HandleMessage(testutil.WithCLIConversation(t.Context()), agent.MessageRequest{Text: "hello"}); err != nil {
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
	triggered := make(chan events.LogRecord, 8)
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if record.EventName == "hook_triggered" {
			triggered <- record
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	if err := os.WriteFile(filepath.Join(dir, "hooks.toml"), []byte(strings.ReplaceAll(rule, " [hook]", " [reloaded]")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.HandleMessage(testutil.WithCLIConversation(t.Context()), agent.MessageRequest{Text: "/hooks reload"}); err != nil {
		t.Fatal(err)
	}
	if err := a.HandleMessage(testutil.WithCLIConversation(t.Context()), agent.MessageRequest{Text: "hello again"}); err != nil {
		t.Fatal(err)
	}
	platform.mu.Lock()
	output = strings.Join(platform.outputs, "\n")
	platform.mu.Unlock()
	if !strings.Contains(output, "answer [reloaded]") {
		t.Fatalf("reloaded output=%q", output)
	}
	logged := false
	deadline := time.After(time.Second)
	for !logged {
		var record events.LogRecord
		select {
		case record = <-triggered:
		case <-deadline:
			t.Fatal("replacement hook log was not delivered")
		}
		var content map[string]any
		if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
			t.Fatal(err)
		}
		if content["hook"] == "integration_suffix" && content["after_text"] == "answer [reloaded]" {
			logged = true
		}
	}
}
