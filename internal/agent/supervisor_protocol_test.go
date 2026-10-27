package agent

import (
	"context"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	api "elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"
)

type contractResponse struct {
	*api.Client
	requests []api.PreparedRequest
}

func (c *contractResponse) StreamPrepared(_ context.Context, req api.PreparedRequest) (<-chan api.Event, error) {
	c.requests = append(c.requests, req)
	raw := `{"id":"response-1","status":"completed","output":[{"type":"function_call","id":"item-1","call_id":"contract-call","name":"contract_echo","arguments":"{}","status":"completed"}]}`
	if len(c.requests) > 1 {
		raw = `{"id":"response-2","status":"completed","output":[{"type":"message","id":"item-2","role":"assistant","status":"completed","content":[{"type":"output_text","text":"native answer"}]}]}`
	}
	var response api.Response
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return nil, err
	}
	ch := make(chan api.Event, 1)
	ch <- api.Event{Type: "response.completed", Response: &response}
	close(ch)
	return ch, nil
}
func TestSupervisorNativeResponseToolTurn(t *testing.T) {
	deps := isolatedModuleDeps(t)
	echo := &contractTool{}
	if err := deps.Tools.(isolatedToolCapabilities).Registry.Register(echo); err != nil {
		t.Fatal(err)
	}
	base, err := api.New("https://unused.invalid", "", nil, nil, api.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client := &contractResponse{Client: base}
	models, err := modelmgr.New(modelmgr.Options{Clients: map[string]llm.Client{"p": client}, Providers: map[string]config.ProviderConfig{"p": {APIMode: "response", Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, DefaultMode: "work"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.Close(context.Background()) })
	deps.Models = models
	m := contractSupervisor(t, deps)
	if err := m.HandleMessage(t.Context(), MessageRequest{Text: "@tool:contract_echo hello"}); err != nil {
		t.Fatal(err)
	}
	if echo.calls != 1 || len(client.requests) != 2 {
		t.Fatalf("calls=%d requests=%d", echo.calls, len(client.requests))
	}
	if !slices.Contains(client.requests[0].AllowedTools(), echo.Name()) {
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
	if messages[len(messages)-1].Content != "native answer" || !slices.ContainsFunc(messages, func(v storage.Message) bool { return v.ToolCallID == "contract-call" && v.Content == "contract result" }) {
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
