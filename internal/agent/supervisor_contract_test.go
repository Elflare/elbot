package agent

import (
	"context"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func contractReadFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(path, []byte("contract result"), 0600); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	return string(args)
}

type contractChat struct {
	isolatedClient
	requests []chatcompletions.Request
	call     llm.ToolCallRequest
}

func (v *contractChat) Stream(_ context.Context, req chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	v.requests = append(v.requests, req)
	ch := make(chan chatcompletions.Chunk, 1)
	if len(v.requests) == 1 {
		ch <- chatcompletions.Chunk{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "contract-call", Name: v.call.Name, Args: v.call.Arguments}}}
	} else {
		ch <- chatcompletions.Chunk{DeltaContent: "contract answer"}
	}
	close(ch)
	return ch, nil
}

func contractModels(t *testing.T, deps *SupervisorDeps, client llm.Client) {
	t.Helper()
	models, err := newConfiguredTestModels(t, testModelOptions{Clients: map[string]llm.Client{"p": client}, Providers: map[string]config.ProviderConfig{"p": {Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, DefaultMode: "work"})
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
	client := &contractChat{call: llm.ToolCallRequest{Name: "read_file", Arguments: contractReadFile(t)}}
	contractModels(t, &deps, client)
	m := contractSupervisor(t, deps)
	items, err := m.Complete(t.Context(), completion.Request{Text: "@tool:read_file"})
	if err != nil || !slices.Contains(completion.Texts(items), "@tool:read_file") {
		t.Fatalf("completion: %v %v", items, err)
	}
	if err := m.HandleMessage(t.Context(), MessageRequest{Text: "@tool:read_file hello"}); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests=%d", len(client.requests))
	}
	if !slices.ContainsFunc(client.requests[0].Tools, func(v llm.ToolSchema) bool { return v.Name == "read_file" }) {
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
	if !slices.ContainsFunc(messages, func(v storage.Message) bool {
		return v.ToolCallID == "contract-call" && strings.Contains(v.Content, "contract result")
	}) {
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
	var received []BindingChangedEvent
	mutator, err := m.ObserveBindingChanged(func(e BindingChangedEvent) error {
		if e.New != nil {
			e.New.SessionID = "corrupted"
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer mutator.Disconnect()
	observer, err := m.ObserveBindingChanged(func(e BindingChangedEvent) error {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, e)
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.HandleMessage(t.Context(), MessageRequest{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	observer.Disconnect()
	mu.Lock()
	before := len(received)
	if before == 0 || received[0].New == nil || received[0].New.SessionID == "corrupted" {
		t.Errorf("binding snapshots: %#v", received)
	}
	mu.Unlock()
	if err := m.HandleMessage(t.Context(), MessageRequest{Text: "/new"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != before {
		t.Fatal("disconnected observer was called")
	}
}
