package agent

import (
	"context"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSupervisorNativeResponseToolTurn(t *testing.T) {
	deps := isolatedModuleDeps(t)
	args := contractReadFile(t)
	var requests []struct {
		ToolChoice struct{ Tools []struct{ Name string } } `json:"tool_choice"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ToolChoice struct{ Tools []struct{ Name string } } `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		requests = append(requests, req)
		raw := fmt.Sprintf(`{"id":"response-1","status":"completed","output":[{"type":"function_call","id":"item-1","call_id":"contract-call","name":"read_file","arguments":%q,"status":"completed"}]}`, args)
		if len(requests) > 1 {
			raw = `{"id":"response-2","status":"completed","output":[{"type":"message","id":"item-2","role":"assistant","status":"completed","content":[{"type":"output_text","text":"native answer"}]}]}`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", raw)
	}))
	t.Cleanup(server.Close)
	models, err := modelmgr.NewModelManager(t.Context(), &config.Config{Providers: map[string]config.ProviderConfig{"p": {BaseURL: server.URL, APIMode: "response", Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.Close(context.Background()) })
	deps.Models = models
	m := contractSupervisor(t, deps)
	if err := m.HandleMessage(t.Context(), MessageRequest{Text: "@tool:read_file hello"}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	if !slices.ContainsFunc(requests[0].ToolChoice.Tools, func(tool struct{ Name string }) bool { return tool.Name == "read_file" }) {
		t.Fatal("native schema missing")
	}
	rows, err := deps.Store.Sessions().List(t.Context(), storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	messages, err := deps.Store.Messages().ListBySession(t.Context(), rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if messages[len(messages)-1].Content != "native answer" || !slices.ContainsFunc(messages, func(v storage.Message) bool {
		return v.ToolCallID == "contract-call" && strings.Contains(v.Content, "contract result")
	}) {
		t.Fatalf("native transcript: %#v", messages)
	}
}

type contractUsageClient struct{ isolatedClient }

func (contractUsageClient) Stream(context.Context, chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	ch := make(chan chatcompletions.Chunk, 1)
	ch <- chatcompletions.Chunk{DeltaContent: "answer", Usage: &llm.Usage{TotalTokens: new(int64(42))}}
	close(ch)
	return ch, nil
}
func TestSupervisorStatusSnapshotIsolation(t *testing.T) {
	deps := isolatedModuleDeps(t)
	contractModels(t, &deps, contractUsageClient{})
	m := contractSupervisor(t, deps)
	var mu sync.Mutex
	var totals []int64
	mutate, err := m.ObserveStatusChanged(func(e StatusChangedEvent) error {
		if e.Snapshot.Usage != nil && e.Snapshot.Usage.TotalTokens != nil {
			*e.Snapshot.Usage.TotalTokens = -1
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer mutate.Disconnect()
	observe, err := m.ObserveStatusChanged(func(e StatusChangedEvent) error {
		if e.Snapshot.Usage != nil && e.Snapshot.Usage.TotalTokens != nil {
			mu.Lock()
			totals = append(totals, *e.Snapshot.Usage.TotalTokens)
			mu.Unlock()
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.HandleMessage(t.Context(), MessageRequest{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := observe.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	before := len(totals)
	if !slices.Contains(totals, 42) || slices.Contains(totals, -1) {
		t.Errorf("usage snapshots: %v", totals)
	}
	mu.Unlock()
	if err := m.HandleMessage(t.Context(), MessageRequest{Text: "again"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(totals) != before {
		t.Fatal("closed status observer called")
	}
}

type contractBlockingClient struct {
	isolatedClient
	entered chan context.Context
}

func (c *contractBlockingClient) Stream(ctx context.Context, _ chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	c.entered <- ctx
	ch := make(chan chatcompletions.Chunk)
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}
func TestSupervisorStopCancelsActiveMessage(t *testing.T) {
	deps := isolatedModuleDeps(t)
	client := &contractBlockingClient{entered: make(chan context.Context, 1)}
	contractModels(t, &deps, client)
	m := contractSupervisor(t, deps)
	result := make(chan error, 1)
	go func() { result <- m.HandleMessage(t.Context(), MessageRequest{Text: "hello"}) }()
	var call context.Context
	select {
	case call = <-client.entered:
	case <-time.After(time.Second):
		t.Fatal("model not started")
	}
	if err := m.HandleMessage(t.Context(), MessageRequest{Text: "/stop"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-call.Done():
	case <-time.After(time.Second):
		t.Fatal("model not canceled")
	}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("message not finished")
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.Done():
	default:
		t.Fatal("supervisor did not stop")
	}
}

func TestSupervisorHookEntrypoints(t *testing.T) {
	m := contractSupervisor(t, isolatedModuleDeps(t))
	var woken bool
	_, err := m.RegisterHooks(Points{
		PlatformMessageReceived: func(_ context.Context, facts Facts, message Message) (PlatformMessageReceivedResult, events.LogResult, error) {
			woken = facts.Woken
			return PlatformMessageReceivedResult{Segments: message.Segments, Consume: true}, events.LogResult{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{ConversationKind: contextinfo.ConversationGroup}})
	for _, tc := range []struct {
		text string
		want bool
	}{{"hello", false}, {"wake hello", true}, {"/stop", true}} {
		if err := m.HandleMessage(ctx, MessageRequest{Text: tc.text, TriggerKeywords: []string{"wake"}}); err != nil {
			t.Fatal(err)
		}
		got := woken
		if got != tc.want {
			t.Errorf("wakeup %q=%v", tc.text, got)
		}
	}
	tracked, done := m.StartHookRequest(t.Context(), "", "contract-hook")
	if facts, ok := contextinfo.ExecutionFromContext(tracked); !ok || facts.RequestID == "" {
		t.Fatal("hook request not tracked")
	}
	done()
	select {
	case <-tracked.Done():
	default:
		t.Fatal("finished hook context alive")
	}
}
