package module

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type retryCall struct {
	ctx     context.Context
	release chan struct{}
}
type retryModel struct {
	notify func(context.Context, llm.RetryEvent)
	calls  chan retryCall
}

func (m *retryModel) SetRetryNotifier(fn func(context.Context, llm.RetryEvent)) { m.notify = fn }
func (*retryModel) ListModels(context.Context) ([]string, error)                { return []string{"first"}, nil }
func (m *retryModel) Stream(ctx context.Context, _ chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	ctx, cancel := context.WithCancel(ctx)
	call := retryCall{ctx: ctx, release: make(chan struct{})}
	m.notify(ctx, llm.RetryEvent{Attempt: 1, MaxRetries: 2, Delay: time.Millisecond, Err: errors.New("retryable")})
	m.calls <- call
	stream := make(chan chatcompletions.Chunk)
	go func() {
		defer close(stream)
		defer cancel()
		select {
		case <-ctx.Done():
			return
		case <-call.release:
		}
		select {
		case stream <- chatcompletions.Chunk{DeltaContent: "summary and title"}:
		case <-ctx.Done():
		}
	}()
	return stream, nil
}

type retryNoticePlatform struct {
	assemblyPlatform
	retryNotices chan string
}

func (p *retryNoticePlatform) SendTo(ctx context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	text := delivery.FallbackOutput(noticeOutputs).Text
	if strings.Contains(text, "正在重试") {
		info, _ := contextinfo.ConversationFromContext(ctx)
		p.retryNotices <- info.Source.ScopeID
	}
	return p.assemblyPlatform.SendTo(ctx, noticeTarget, noticeOutputs)
}

func TestSharedRetrySubscriptionCoversChatCompactAndNaming(t *testing.T) {
	deps := isolatedModuleDeps(t)
	deps.Context = config.Default().Context
	model := &retryModel{calls: make(chan retryCall, 4)}
	p := &retryNoticePlatform{retryNotices: make(chan string, 5)}
	models, err := modelmgr.New(modelmgr.Options{Clients: map[string]llm.Client{"p": model}, Providers: map[string]config.ProviderConfig{"p": {Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, DefaultMode: "work"})
	if err != nil {
		t.Fatal(err)
	}
	defer models.Close(context.Background())
	deps.Models = models
	deps.Dispatcher = dispatch.New(dispatch.Options{Primary: p, Store: deps.Store})
	connectTestNotifications(t, deps.Dispatcher)
	runtime, err := NewModule(t.Context(), deps)
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	if err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "local"}, Identity: contextinfo.Identity{PlatformUserID: "local"}})
	ctx = contextinfo.WithModel(ctx, contextinfo.Model{Provider: "p", Model: "m", APIType: "chat"})
	for _, name := range []string{"chat", "compact", "naming"} {
		result := make(chan error, 1)
		go func() {
			switch name {
			case "chat":
				result <- runtime.Handler().HandleMessage(ctx, "hello")
			case "compact":
				_, err := runtime.Execution().CompactCurrent(ctx, "test")
				result <- err
			case "naming":
				_, err := session.NewTitleGenerator(deps.Models).GenerateTitle(ctx, []storage.Message{{Role: storage.RoleUser, Content: "hello"}})
				result <- err
			}
		}()
		var call retryCall
		select {
		case call = <-model.calls:
		case <-time.After(time.Second):
			t.Fatalf("%s did not invoke shared client", name)
		}
		select {
		case scope := <-p.retryNotices:
			if scope != "local" {
				t.Fatal("lost original source", scope)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s retry never reached subscriber", name)
		}
		close(call.release)
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not finish", name)
		}
		select {
		case <-call.ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("call context remained alive")
		}
	}
	if len(p.retryNotices) != 0 {
		t.Fatal("duplicate retries")
	}
	// A retry belonging to a finished call must not reach a platform.
	finished, cancel := context.WithCancel(ctx)
	cancel()
	model.notify(finished, llm.RetryEvent{Attempt: 1, MaxRetries: 2, Err: errors.New("retryable")})
	if ctx.Err() != nil {
		t.Fatal("test canceled parent instead of call")
	}
	if len(p.retryNotices) != 0 {
		t.Fatal("finished call displayed stale retry")
	}
}

func (m *retryModel) GenerateText(ctx context.Context, req llm.TextRequest) (llm.TextResult, error) {
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
