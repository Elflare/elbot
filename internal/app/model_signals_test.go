package app

import (
	"context"
	"elbot/internal/llm/chatcompletions"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/platform"
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

func (p *retryNoticePlatform) SendNotice(ctx context.Context, notice delivery.Notice) (delivery.Receipt, error) {
	text := delivery.FallbackOutput(notice.Outputs).Text
	if strings.Contains(text, "正在重试") {
		info, _ := contextinfo.ConversationFromContext(ctx)
		p.retryNotices <- info.Source.ScopeID
	}
	return p.assemblyPlatform.SendNotice(ctx, notice)
}

func TestSharedRetrySubscriptionCoversChatCompactAndNaming(t *testing.T) {
	req, _, _ := runtimeAssemblyFixture(t)
	model := &retryModel{calls: make(chan retryCall, 4)}
	p := &retryNoticePlatform{retryNotices: make(chan string, 5)}
	req.Models.ByProvider["test"] = model
	req.Platforms = PlatformComponents{Primary: p, Runtimes: []platform.Runtime{p}}
	runtime, err := (defaultRuntimeFactory{}).Build(context.Background(), req)
	t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
	if err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "local"}, Identity: contextinfo.Identity{PlatformUserID: "local"}})
	for _, name := range []string{"chat", "compact", "naming"} {
		result := make(chan error, 1)
		go func() {
			switch name {
			case "chat":
				result <- runtime.Agent.HandleMessage(ctx, "hello")
			case "compact":
				_, err := runtime.Agent.CompactCurrent(ctx, "test")
				result <- err
			case "naming":
				_, err := session.NewTitleGenerator(runtime.Models).GenerateTitle(ctx, []storage.Message{{Role: storage.RoleUser, Content: "hello"}})
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
	// Delay a retry until its own call finishes while the parent remains alive.
	// Hold every observer queue so this barrier does not depend on assembly order.
	releases := make([]func(), 0, len(runtime.Signals.queues))
	for _, queue := range runtime.Signals.queues {
		releases = append(releases, holdObserverQueue(t, queue))
	}
	stream, err := runtime.Models.ClientForProvider("test").(chatcompletions.Streamer).Stream(ctx, chatcompletions.Request{Model: "first"})
	if err != nil {
		t.Fatal(err)
	}
	call := <-model.calls
	close(call.release)
	for range stream {
	}
	<-call.ctx.Done()
	if ctx.Err() != nil {
		t.Fatal("test canceled parent instead of call")
	}
	for _, release := range releases {
		release()
	}
	for _, queue := range runtime.Signals.queues {
		flushObserverQueue(t, queue)
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
