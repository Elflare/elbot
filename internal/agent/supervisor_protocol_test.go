package agent

import (
	"context"
	"elbot/internal/testutil"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/session"
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
	for _, tc := range []struct {
		name           string
		toolRounds     int
		invalidSummary bool
	}{
		{"normal", 1, false}, {"summary", 3, false}, {"summary rejects tool", 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := isolatedModuleDeps(t)
			args := contractReadFile(t)
			var requests []json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ToolChoice json.RawMessage `json:"tool_choice"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					http.Error(w, "bad request", 400)
					return
				}
				requests = append(requests, req.ToolChoice)
				raw := fmt.Sprintf(`{"id":"response-1","status":"completed","output":[{"type":"function_call","id":"item-1","call_id":"contract-call-%d","name":"read_file","arguments":%q,"status":"completed"}]}`, len(requests), args)
				if len(requests) > tc.toolRounds && !tc.invalidSummary {
					raw = `{"id":"response-2","status":"completed","output":[{"type":"message","id":"item-2","role":"assistant","status":"completed","content":[{"type":"output_text","text":"native answer"}]}]}`
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", raw)
			}))
			t.Cleanup(server.Close)
			models, err := modelmgr.NewModelManager(t.Context(), &config.Config{Providers: map[string]config.ProviderConfig{"p": {BaseURL: server.URL, APIMode: "response", Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, LLMRequest: config.LLMRequestConfig{FirstChunkTimeoutSeconds: 60, StreamIdleTimeoutSeconds: 60}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = models.Close(context.Background()) })
			deps.Models = models
			m := contractSupervisor(t, deps)
			ctx := testutil.WithCLIConversation(t.Context())
			info, _ := contextinfo.ConversationFromContext(ctx)
			info.InputID = "echo-1"
			err = m.HandleMessage(contextinfo.WithConversation(ctx, info), MessageRequest{Text: "@tool:read_file hello"})
			if tc.invalidSummary {
				if err == nil || !strings.Contains(err.Error(), "function") {
					t.Fatalf("expected tool rejection, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(requests) != tc.toolRounds+1 {
				t.Fatalf("requests=%d", len(requests))
			}
			var choice struct{ Tools []struct{ Name string } }
			if err := json.Unmarshal(requests[0], &choice); err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(choice.Tools, func(tool struct{ Name string }) bool { return tool.Name == "read_file" }) {
				t.Fatal("native schema missing")
			}
			if tc.toolRounds == 3 && string(requests[3]) != `"none"` {
				t.Fatal("summary enabled tools")
			}
			if tc.invalidSummary {
				return
			}
			rows, err := deps.Store.Sessions().List(t.Context(), storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 10})
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			messages, err := deps.Store.Messages().ListBySession(t.Context(), rows[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			var meta storage.UserInputMetadata
			if err := json.Unmarshal([]byte(messages[0].Metadata), &meta); err != nil || !slices.Equal(meta.InputIDs, []string{"echo-1"}) {
				t.Fatalf("lost input ID: %s (%v)", messages[0].Metadata, err)
			}
			if messages[len(messages)-1].Content != "native answer" || !slices.ContainsFunc(messages, func(v storage.Message) bool {
				return v.ToolCallID == "contract-call-1" && strings.Contains(v.Content, "contract result")
			}) {
				t.Fatalf("native transcript: %#v", messages)
			}
		})
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
	mutate, err := m.ObserveStatusChanged(func(e StatusChangedEvent) {
		if e.Snapshot.Usage != nil && e.Snapshot.Usage.TotalTokens != nil {
			*e.Snapshot.Usage.TotalTokens = -1
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer mutate.Disconnect()
	observe, err := m.ObserveStatusChanged(func(e StatusChangedEvent) {
		if e.Snapshot.Usage != nil && e.Snapshot.Usage.TotalTokens != nil {
			mu.Lock()
			totals = append(totals, *e.Snapshot.Usage.TotalTokens)
			mu.Unlock()
		}

	}, signal.ConnectOptions{Shutdown: signal.Drain})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.HandleMessage(testutil.WithCLIConversation(t.Context()), MessageRequest{Text: "hello"}); err != nil {
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
	if err := m.HandleMessage(testutil.WithCLIConversation(t.Context()), MessageRequest{Text: "again"}); err != nil {
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
	go func() {
		result <- m.HandleMessage(testutil.WithCLIConversation(t.Context()), MessageRequest{Text: "hello"})
	}()
	var call context.Context
	select {
	case call = <-client.entered:
	case <-time.After(time.Second):
		t.Fatal("model not started")
	}
	if err := m.HandleMessage(testutil.WithCLIConversation(t.Context()), MessageRequest{Text: "/stop"}); err != nil {
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

func TestAddressedTasksAndStopAreIndependent(t *testing.T) {
	deps := isolatedModuleDeps(t)
	client := &contractBlockingClient{entered: make(chan context.Context, 2)}
	contractModels(t, &deps, client)
	m := contractSupervisor(t, deps)
	base, cancel := context.WithCancel(testutil.WithCLIConversation(t.Context()))
	defer cancel()
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}, Platform: "cli", ChatID: "local", ChatType: contextinfo.ChatPrivate}
	create := func() context.Context {
		t.Helper()
		created, err := m.sessions.Create(base, scope, session.CreateRequest{})
		if err != nil || created.Status != operationresult.Success {
			t.Fatal(created, err)
		}
		info, _ := contextinfo.ConversationFromContext(base)
		info.TargetSessionID = created.Data.ID
		return contextinfo.WithConversation(base, info)
	}
	first, second := create(), create()
	start := func(ctx context.Context) (context.Context, chan error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- m.HandleMessage(ctx, MessageRequest{Text: "hello"}) }()
		select {
		case call := <-client.entered:
			return call, done
		case err := <-done:
			t.Fatalf("input exited early: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("model did not start")
		}
		return nil, nil
	}
	firstCall, firstDone := start(first)
	secondCall, secondDone := start(second)
	stop := func(ctx, call context.Context, done chan error) {
		t.Helper()
		if err := m.HandleMessage(ctx, MessageRequest{Text: "/stop"}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-call.Done():
		case <-time.After(time.Second):
			t.Fatal("target not stopped")
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("task did not finish")
		}
	}
	stop(second, secondCall, secondDone)
	if firstCall.Err() != nil {
		t.Fatal("second stop canceled first task")
	}
	stop(first, firstCall, firstDone)
}

func TestSupervisorHookEntrypoints(t *testing.T) {
	m := contractSupervisor(t, isolatedModuleDeps(t))
	var woken bool
	_, err := m.RegisterHooks(Points{
		PlatformMessageReceived: func(_ context.Context, facts Facts, message Message) (PlatformMessageReceivedResult, error) {
			woken = facts.Woken
			return PlatformMessageReceivedResult{Segments: message.Segments, Consume: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{ChatType: contextinfo.ChatGroup}})
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
	tracked, done, _ := m.StartHookRequest(t.Context(), "", "contract-hook")
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
