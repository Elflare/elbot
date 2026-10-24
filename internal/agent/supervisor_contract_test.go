package agent

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"slices"
	"sync"
	"testing"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
	"elbot/internal/storage"
	
)

type contractTool struct{ calls int }

func (*contractTool) Name() string { return "contract_echo" }
func (v *contractTool) Info() toolshare.Info {
	return toolshare.Info{Name: v.Name(), Source: toolshare.SourceBuiltin, Risk: toolshare.RiskSafe, Tags: []string{"contract"}}
}
func (v *contractTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: v.Name(), Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}
}
func (v *contractTool) Call(context.Context, toolshare.CallRequest) (*toolshare.Result, string, error) {
	v.calls++
	return &toolshare.Result{Content: "contract result"}, "", nil
}

type contractChat struct {
	isolatedClient
	requests []chatcompletions.Request
}

func (v *contractChat) Stream(_ context.Context, req chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	v.requests = append(v.requests, req)
	ch := make(chan chatcompletions.Chunk, 1)
	if len(v.requests) == 1 {
		ch <- chatcompletions.Chunk{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "contract-call", Name: "contract_echo", Args: "{}"}}}
	} else {
		ch <- chatcompletions.Chunk{DeltaContent: "contract answer"}
	}
	close(ch)
	return ch, nil
}

func contractModels(t *testing.T, deps *SupervisorDeps, client llm.Client) {
	t.Helper()
	models, err := modelmgr.New(modelmgr.Options{Clients: map[string]llm.Client{"p": client}, Providers: map[string]config.ProviderConfig{"p": {Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, DefaultMode: "work"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.Close(context.Background()) })
	deps.Models = models
}

func contractSupervisor(t *testing.T, deps SupervisorDeps) *Supervisor {
	t.Helper()
	connectTestNotifications(t, deps.Platform.(isolatedPlatformCapabilities).Router)
	m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSupervisorToolPreloadExecutionAndPersistence(t *testing.T) {
	deps := isolatedModuleDeps(t)
	echo := &contractTool{}
	if err := deps.Tools.(isolatedToolCapabilities).Registry.Register(echo); err != nil {
		t.Fatal(err)
	}
	client := &contractChat{}
	contractModels(t, &deps, client)
	m := contractSupervisor(t, deps)
	items, err := m.Complete(t.Context(), completion.Request{Text: "@tool:contract"})
	if err != nil || !slices.Contains(completion.Texts(items), "@tool:contract_echo") || !slices.Contains(completion.Texts(items), "@tool:contract") {
		t.Fatalf("completion: %v %v", items, err)
	}
	if err := m.HandleMessage(t.Context(), "@tool:contract_echo hello"); err != nil {
		t.Fatal(err)
	}
	if echo.calls != 1 || len(client.requests) != 2 {
		t.Fatalf("calls=%d requests=%d", echo.calls, len(client.requests))
	}
	if !slices.ContainsFunc(client.requests[0].Tools, func(v llm.ToolSchema) bool { return v.Name == echo.Name() }) {
		t.Fatal("preloaded schema missing")
	}
	rows, err := deps.Store.Sessions().List(t.Context(), storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	messages, err := deps.Store.Messages().ListBySession(t.Context(), rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(messages, func(v storage.Message) bool { return v.ToolCallID == "contract-call" && v.Content == "contract result" }) {
		t.Fatalf("missing persisted tool result: %#v", messages)
	}
	if messages[len(messages)-1].Content != "contract answer" {
		t.Fatal(messages)
	}
}

func TestSupervisorBindingSubscriptionsAndCommandMetadata(t *testing.T) {
	deps := isolatedModuleDeps(t)
	m := contractSupervisor(t, deps)
	commands := m.Commands()
	found := false
	for i := range commands {
		if len(commands[i].Aliases) > 0 {
			commands[i].Aliases[0] = "corrupted"
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fixture has no command aliases")
	}
	for _, cmd := range m.Commands() {
		if slices.Contains(cmd.Aliases, "corrupted") {
			t.Fatal("command aliases shared")
		}
	}
	var mu sync.Mutex
	var received []agentevents.BindingChangedEvent
	mutator, err := m.ObserveBindingChanged(func(_ context.Context, e agentevents.BindingChangedEvent) error {
		if e.New != nil {
			e.New.SessionID = "corrupted"
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer mutator.Disconnect()
	observer, err := m.ObserveBindingChanged(func(_ context.Context, e agentevents.BindingChangedEvent) error {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, e)
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.HandleMessage(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	observer.Disconnect()
	mu.Lock()
	before := len(received)
	if before == 0 || received[0].New == nil || received[0].New.SessionID == "corrupted" {
		t.Errorf("binding snapshots: %#v", received)
	}
	mu.Unlock()
	if err := m.HandleMessage(t.Context(), "/new"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != before {
		t.Fatal("disconnected observer was called")
	}
}
