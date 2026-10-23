package execution

import (
	"context"
	"errors"

	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"

	"elbot/internal/session"

	"elbot/internal/storage"
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

type failingNamingClient struct {
	started chan struct{}
	release chan struct{}
	request chan chatcompletions.Request
}

func (c *failingNamingClient) ListModels(context.Context) ([]string, error) { return nil, nil }

func (c *failingNamingClient) Stream(ctx context.Context, req chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	c.request <- req
	close(c.started)
	select {
	case <-c.release:
		return nil, errors.New("naming unavailable")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestNamingFallbackKeepsContextSelectionAcrossSwitchAndReload(t *testing.T) {
	started, release, unblock := modelBarrier(t)
	naming := &failingNamingClient{started: started, release: release, request: make(chan chatcompletions.Request, 1)}
	old := &fakeLLM{titleReplies: []string{"old title"}}
	next := &fakeLLM{titleReplies: []string{"new title"}}
	clients := map[string]llm.Client{"naming": naming, "old": old, "next": next}
	providers := map[string]config.ProviderConfig{"naming": {Models: []string{"title"}}, "old": {Models: []string{"first"}}, "next": {Models: []string{"second"}}}
	models := newTestModels(t, modelmgr.Options{
		Clients:     clients,
		Providers:   providers,
		ModeModels:  map[string]config.ModelSelection{storage.SessionModeWork: {Provider: "old", Model: "first"}},
		NamingModel: config.ModelSelection{Provider: "naming", Model: "title"},
		LoadProviders: func(context.Context) (config.ProviderFile, error) {
			return config.ProviderFile{Providers: providers}, nil
		},
		BuildClient: func(name string, _ config.ProviderConfig) (llm.Client, error) {
			if name == "old" {
				return next, nil
			}
			return clients[name], nil
		},
	})
	g := session.NewTitleGenerator(models)
	messages := []storage.Message{{Role: storage.RoleUser, Content: "name this"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = contextinfo.WithModel(ctx, contextinfo.Model{Provider: "old", Model: "first", APIType: "chat"})
	done := make(chan error, 1)
	go func() {
		result, err := g.GenerateTitle(ctx, messages)
		if err == nil && (result.RawTitle != "old title" || result.Provider != "old" || result.Model != "first") {
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
	if err := models.ReloadProviders(ctx); err != nil {
		t.Fatal(err)
	}
	unblock()
	awaitModelDone(t, done)
	if req := <-naming.request; req.Model != "title" {
		t.Fatalf("naming model = %q", req.Model)
	}
	old.mu.Lock()
	oldRequests := append([]chatcompletions.Request(nil), old.requests...)
	old.mu.Unlock()
	if len(oldRequests) != 1 || oldRequests[0].Model != "first" || next.requestCount() != 0 {
		t.Fatalf("fallback switched: %#v", oldRequests)
	}
	result, err := g.GenerateTitle(ctx, messages)
	if err != nil || result.RawTitle != "new title" || result.Provider != "next" || result.Model != "second" {
		t.Fatalf("new naming = %#v, %v", result, err)
	}
}

func (c *failingNamingClient) GenerateText(ctx context.Context, req llm.TextRequest) (llm.TextResult, error) {
	messages := []llm.LLMMessage{}
	if req.Instructions != "" {
		messages = append(messages, llm.LLMMessage{Role: llm.RoleSystem, Segments: llm.TextSegments(req.Instructions)})
	}
	messages = append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments(req.Input)})
	chunks, err := c.Stream(ctx, chatcompletions.Request{Model: req.Model, Messages: messages, MaxTokens: req.MaxOutputTokens, ExtraBody: req.ExtraBody})
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
