package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

func modelBarrier(t *testing.T) (chan struct{}, chan struct{}, func()) {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	return started, release, unblock
}

func awaitModelBarrier(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("model request did not reach barrier")
	}
}

func awaitModelDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("model operation did not finish")
	}
}

func TestModelCommandKeepsInFlightToolLoopOnOriginalSelection(t *testing.T) {
	started, release, unblock := modelBarrier(t)
	old := &fakeLLM{
		chatBlocks: []fakeLLMBlock{{started: started, release: release}},
		chunks: [][]llm.StreamChunk{
			{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call", Name: "prepared_args", Args: `{}`}}, FinishReason: "tool_calls"}},
			{{DeltaContent: "old reply"}},
		},
	}
	next := &fakeLLM{replies: []string{"new reply"}}
	models := newTestModels(t, modelmgr.Options{
		Clients:    map[string]llm.LLM{"old": old, "next": next},
		Providers:  map[string]config.ProviderConfig{"old": {Models: []string{"first"}}, "next": {Models: []string{"second"}}},
		ModeModels: map[string]config.ModelSelection{storage.SessionModeWork: {Provider: "old", Model: "first"}},
	})
	opts := validConstructorOptions(t)
	opts.Models, opts.Platform = models, &fakePlatform{}
	opts.SecurityPolicy = security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}})
	// Naming has its own snapshot boundary and is not part of this assertion.
	opts.SessionConfig.NamingConfig.TriggerStep = 100

	registry := tool.NewRegistry()
	arguments := ""
	if err := registry.Register(preparedArgumentTool{arguments: &arguments}); err != nil {
		t.Fatal(err)
	}
	a := mustNewWithOptions(t, opts, func(cfg *testAgentOptions) {
		cfg.ToolRegistry = registry
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "use the tool") }()
	awaitModelBarrier(t, started)
	if err := a.HandleMessage(ctx, "/model --work next/second"); err != nil {
		t.Fatal(err)
	}
	if models.ResolveMode(storage.SessionModeWork).Model != "second" {
		t.Fatal("command did not update the injected service")
	}
	unblock()
	awaitModelDone(t, done)
	requests := old.chatRequests()
	if len(requests) != 2 || requests[0].Model != "first" || requests[1].Model != "first" || next.requestCount() != 0 {
		t.Fatalf("in-flight loop switched: old=%#v next=%d", requests, next.requestCount())
	}
	if arguments != "{}" {
		t.Fatalf("tool was not executed: %q", arguments)
	}
	if err := a.HandleMessage(ctx, "next turn"); err != nil {
		t.Fatal(err)
	}
	if got := next.chatRequests(); len(got) != 1 || got[0].Model != "second" {
		t.Fatalf("next turn selection = %#v", got)
	}
}

type failingNamingClient struct {
	started chan struct{}
	release chan struct{}
	request chan llm.ChatRequest
}

func (c *failingNamingClient) ListModels(context.Context) ([]string, error) { return nil, nil }
func (c *failingNamingClient) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	c.request <- req
	close(c.started)
	select {
	case <-c.release:
		return nil, errors.New("naming unavailable")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestNamingFallbackKeepsOriginalWorkSelectionAcrossSwitch(t *testing.T) {
	started, release, unblock := modelBarrier(t)
	naming := &failingNamingClient{started: started, release: release, request: make(chan llm.ChatRequest, 1)}
	old := &fakeLLM{titleReplies: []string{"old title"}}
	next := &fakeLLM{titleReplies: []string{"new title"}}
	models := newTestModels(t, modelmgr.Options{
		Clients:     map[string]llm.LLM{"naming": naming, "old": old, "next": next},
		Providers:   map[string]config.ProviderConfig{"naming": {Models: []string{"title"}}, "old": {Models: []string{"first"}}, "next": {Models: []string{"second"}}},
		ModeModels:  map[string]config.ModelSelection{storage.SessionModeWork: {Provider: "old", Model: "first"}},
		NamingModel: config.ModelSelection{Provider: "naming", Model: "title"},
	})
	g := session.NewTitleGenerator(models)
	messages := []storage.Message{{Role: storage.RoleUser, Content: "name this"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result, err := g.GenerateTitle(ctx, messages)
		if err == nil && result.RawTitle != "old title" {
			err = errors.New("old naming operation did not use original fallback")
		}
		done <- err
	}()
	awaitModelBarrier(t, started)
	if _, err := models.SelectModelForMode(storage.SessionModeWork, "next/second"); err != nil {
		t.Fatal(err)
	}
	if _, err := models.SelectNamingModel("next/second"); err != nil {
		t.Fatal(err)
	}
	unblock()
	awaitModelDone(t, done)
	if req := <-naming.request; req.Model != "title" {
		t.Fatalf("naming model = %q", req.Model)
	}
	old.mu.Lock()
	oldRequests := append([]llm.ChatRequest(nil), old.requests...)
	old.mu.Unlock()
	if len(oldRequests) != 1 || oldRequests[0].Model != "first" || next.requestCount() != 0 {
		t.Fatalf("fallback switched: %#v", oldRequests)
	}
	result, err := g.GenerateTitle(ctx, messages)
	if err != nil || result.RawTitle != "new title" {
		t.Fatalf("new naming = %#v, %v", result, err)
	}
}

func TestCompactUsesOriginalSelectionDuringModelSwitch(t *testing.T) {
	started, release, unblock := modelBarrier(t)
	old := &fakeLLM{chatBlocks: []fakeLLMBlock{{started: started, release: release}}, replies: []string{"old summary", "continued reply"}}
	next := &fakeLLM{replies: []string{"new summary"}}
	models := newTestModels(t, modelmgr.Options{
		Clients:    map[string]llm.LLM{"old": old, "next": next},
		Providers:  map[string]config.ProviderConfig{"old": {Models: []string{"first"}}, "next": {Models: []string{"second"}}},
		ModeModels: map[string]config.ModelSelection{storage.SessionModeWork: {Provider: "old", Model: "first"}},
	})
	opts := validConstructorOptions(t)
	opts.Models, opts.Platform = models, &fakePlatform{}
	a := mustNewWithOptions(t, opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source, err := a.execution.sessions.Create(ctx, a.identity.Scope(ctx), session.CreateRequest{Title: "compact"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.execution.chat.messages.Append(ctx, &storage.Message{SessionID: source.ID, Role: storage.RoleUser, Content: "keep me"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := a.CompactCurrent(ctx, "manual"); done <- err }()
	awaitModelBarrier(t, started)
	if _, err := models.SelectCompactModel("next/second"); err != nil {
		t.Fatal(err)
	}
	unblock()
	awaitModelDone(t, done)
	if got := old.chatRequests(); len(got) != 1 || got[0].Model != "first" || next.requestCount() != 0 {
		t.Fatalf("compact switched: %#v", got)
	}
	// A manual compaction seeds its new Session on the next user input.
	if err := a.HandleMessage(ctx, "continue after compaction"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CompactCurrent(ctx, "manual"); err != nil {
		t.Fatal(err)
	}
	if got := next.chatRequests(); len(got) != 1 || got[0].Model != "second" || !strings.Contains(llm.SegmentsContentText(got[0].Messages[1].Segments), "old summary") {
		t.Fatalf("next compact = %#v", got)
	}
}
