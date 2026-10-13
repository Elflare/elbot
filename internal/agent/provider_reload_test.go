package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	api "elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/tool"
)

func TestProviderRefreshKeepsInFlightToolLoopAndUpdatesNextTurn(t *testing.T) {
	started, release, unblock := modelBarrier(t)
	old := &fakeLLM{chatBlocks: []fakeLLMBlock{{started: started, release: release}}, chunks: [][]chatcompletions.Chunk{
		{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "call", Name: "prepared_args", Args: `{}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "old reply"}},
	}}
	next := &fakeLLM{replies: []string{"new reply"}}
	name := "p"
	models := newTestModels(t, modelmgr.Options{
		Clients: map[string]llm.Client{"p": old}, Providers: map[string]config.ProviderConfig{"p": {Models: []string{"m"}}},
		ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}},
		LoadProviders: func(context.Context) (config.ProviderFile, error) {
			return config.ProviderFile{Providers: map[string]config.ProviderConfig{name: {Models: []string{"m"}}}}, nil
		},
		BuildClient: func(string, config.ProviderConfig) (llm.Client, error) { return next, nil },
	})
	opts := validConstructorOptions(t)
	opts.Models = models
	opts.SessionConfig.NamingConfig.TriggerStep = 100
	opts.SecurityPolicy = security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}})
	registry := tool.NewRegistry()
	arguments := ""
	if err := registry.Register(preparedArgumentTool{arguments: &arguments}); err != nil {
		t.Fatal(err)
	}
	opts.ToolRegistry = registry
	a := mustNewWithOptions(t, opts)
	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(t.Context(), "use the tool") }()
	awaitModelBarrier(t, started)
	if err := a.HandleMessage(t.Context(), "/models --refresh"); err != nil {
		t.Fatal(err)
	}
	if models.ResolveMode("work").Client != next {
		t.Fatal("refresh did not publish client")
	}
	unblock()
	awaitModelDone(t, done)
	if old.requestCount() != 2 || next.requestCount() != 0 || arguments != "{}" {
		t.Fatal("in-flight tool loop changed client")
	}
	if err := a.HandleMessage(t.Context(), "next turn"); err != nil {
		t.Fatal(err)
	}
	if next.requestCount() != 1 {
		t.Fatal("next turn did not use refreshed client")
	}
	name = "renamed"
	for _, text := range []string{"/models --refresh", "/model renamed/m", "continue on another Chat provider"} {
		if err := a.HandleMessage(t.Context(), text); err != nil {
			t.Fatal(err)
		}
	}
	if next.requestCount() != 2 {
		t.Fatal("Chat could not continue on the newly configured provider")
	}
}

func TestResponsesRefreshAllowsNewURLAndRejectsRenamedProvider(t *testing.T) {
	var mu sync.Mutex
	var captured []nativeTestRequest
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
			return
		}
		var req nativeTestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		captured = append(captured, req)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		emitNative(w, "new-response", "completed", nativeText("new domain reply"))
	}))
	defer next.Close()
	name := "native"
	f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
		emitNative(w, "old-response", "completed", nativeText("old domain reply"))
	}, func(opts *testAgentOptions) {
		old := opts.Models.ResolveMode("work")
		opts.Models = newTestModels(t, modelmgr.Options{
			Clients: map[string]llm.Client{"native": old.Client}, Providers: map[string]config.ProviderConfig{"native": {APIMode: "response", BaseURL: old.Origin.BaseURL, Models: []string{"m"}}},
			ModeModels: map[string]config.ModelSelection{"work": {Provider: "native", Model: "m"}},
			LoadProviders: func(context.Context) (config.ProviderFile, error) {
				return config.ProviderFile{Providers: map[string]config.ProviderConfig{name: {APIMode: "response", BaseURL: next.URL, Models: []string{"m"}}}}, nil
			},
			BuildClient: func(_ string, cfg config.ProviderConfig) (llm.Client, error) {
				return api.New(cfg.BaseURL, cfg.APIKey, nil, nil, api.RequestOptions{})
			},
		})
	})
	if err := f.agent.HandleMessage(t.Context(), "first input"); err != nil {
		t.Fatal(err)
	}
	row := fixtureSession(t, f)
	saved, _, err := session.Origin(row)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "/models --refresh"); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "second input"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	requests := append([]nativeTestRequest(nil), captured...)
	mu.Unlock()
	if len(requests) != 1 || requests[0].PreviousResponseID != "" || len(requests[0].Input) < 3 {
		t.Fatalf("new domain did not replay: %+v", requests)
	}
	current := fixtureSession(t, f)
	if origin, _, err := session.Origin(current); err != nil || origin != saved {
		t.Fatal("reload rewrote historical source", err)
	}
	name = "renamed"
	if err := f.agent.HandleMessage(t.Context(), "/models --refresh"); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "missing provider"); err == nil {
		t.Fatal("deleted provider remained usable")
	}
	if err := f.agent.HandleMessage(t.Context(), "/model renamed/m"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.platform.out.String(), "不兼容") {
		t.Fatal("Responses did not explain incompatible provider")
	}
	if _, err := f.agent.execution.models.SelectModelForMode("work", "renamed/m"); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "incompatible provider"); err == nil || !strings.Contains(err.Error(), "不兼容") {
		t.Fatalf("Responses compatibility=%v", err)
	}
	mu.Lock()
	count := len(captured)
	mu.Unlock()
	if count != 1 {
		t.Fatal("incompatible conversation reached upstream")
	}
}
