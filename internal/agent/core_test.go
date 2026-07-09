package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	agentcommands "elbot/internal/agent/commands"
	"elbot/internal/background"
	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	hookbuiltin "elbot/internal/hook/builtin"
	"elbot/internal/llm"
	"elbot/internal/memory/resident"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"elbot/internal/turn"
)

type fakePlatform struct {
	out         strings.Builder
	preview     strings.Builder
	reasoning   strings.Builder
	statusCount int
	lastStatus  runtimestatus.Snapshot
}

func (p *fakePlatform) Name() string { return "cli" }

func (p *fakePlatform) Run(context.Context, platform.PlatformHandler) error { return nil }

func (p *fakePlatform) SendChat(ctx context.Context, out delivery.Output) (delivery.Receipt, error) {
	p.out.WriteString(delivery.FallbackText(out))
	return delivery.Receipt{}, nil
}

func (p *fakePlatform) SendNotice(ctx context.Context, target delivery.Target, out delivery.Output) (delivery.Receipt, error) {
	text := delivery.FallbackText(out)
	p.out.WriteString(text)
	p.preview.WriteString(text)
	return delivery.Receipt{}, nil
}

func (p *fakePlatform) SendReasoning(ctx context.Context, text string) error {
	p.reasoning.WriteString(text)
	return nil
}

func (p *fakePlatform) SetRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) error {
	p.statusCount++
	p.lastStatus = snapshot
	return nil
}

type fakeStreamingPlatform struct {
	fakePlatform
	stream fakeMessageStream
}

type fakeMessageStream struct {
	appends  []string
	replaces []string
	finished int
}

func (p *fakeStreamingPlatform) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	return &p.stream, nil
}

func (s *fakeMessageStream) Append(ctx context.Context, text string) error {
	s.appends = append(s.appends, text)
	return nil
}

func (s *fakeMessageStream) Replace(ctx context.Context, text string) (delivery.Receipt, error) {
	s.replaces = append(s.replaces, text)
	return delivery.Receipt{}, nil
}

func (s *fakeMessageStream) Finish(ctx context.Context) (delivery.Receipt, error) {
	s.finished++
	return delivery.Receipt{}, nil
}

type fakeLLMBlock struct {
	started chan struct{}
	release chan struct{}
}

type fakeLLM struct {
	models        []string
	replies       []string
	chunks        [][]llm.StreamChunk
	titleReplies  []string
	chatBlocks    []fakeLLMBlock
	requests      []llm.ChatRequest
	requestNotify chan struct{}
	mu            sync.Mutex
}

func (f *fakeLLM) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.notifyRequestLocked()
	isTitle := isTitleRequest(req)
	var block fakeLLMBlock
	if !isTitle && len(f.chatBlocks) > 0 {
		block = f.chatBlocks[0]
		f.chatBlocks = f.chatBlocks[1:]
	}
	reply := ""
	if isTitle {
		if len(f.titleReplies) > 0 {
			reply = f.titleReplies[0]
			f.titleReplies = f.titleReplies[1:]
		} else {
			reply = "generated title"
		}
	} else if len(f.chunks) > 0 {
		chunks := f.chunks[0]
		f.chunks = f.chunks[1:]
		f.mu.Unlock()
		if err := waitFakeLLMBlock(ctx, block); err != nil {
			return fakeLLMErrorStream(err), nil
		}
		ch := make(chan llm.StreamChunk, len(chunks))
		for _, chunk := range chunks {
			ch <- chunk
		}
		close(ch)
		return ch, nil
	} else if len(f.replies) > 0 {
		reply = f.replies[0]
		f.replies = f.replies[1:]
	}
	f.mu.Unlock()
	if err := waitFakeLLMBlock(ctx, block); err != nil {
		return fakeLLMErrorStream(err), nil
	}
	ch := make(chan llm.StreamChunk, 1)
	if reply == "__ERR__" {
		ch <- llm.StreamChunk{Error: fmt.Errorf("fake stream error")}
	} else {
		ch <- llm.StreamChunk{DeltaContent: reply}
	}
	close(ch)
	return ch, nil
}

func waitFakeLLMBlock(ctx context.Context, block fakeLLMBlock) error {
	if block.started != nil {
		close(block.started)
	}
	if block.release == nil {
		return nil
	}
	select {
	case <-block.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func fakeLLMErrorStream(err error) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Error: err}
	close(ch)
	return ch
}

func (f *fakeLLM) ListModels(context.Context) ([]string, error) { return f.models, nil }

func (f *fakeLLM) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeLLM) chatRequests() []llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []llm.ChatRequest{}
	for _, req := range f.requests {
		if !isTitleRequest(req) {
			out = append(out, req)
		}
	}
	return out
}

func isTitleRequest(req llm.ChatRequest) bool {
	return len(req.Messages) > 0 && strings.Contains(llm.SegmentsContentText(req.Messages[0].Segments), "会话命名助手")
}

func containsAll(values []string, want []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range want {
		if !seen[value] {
			return false
		}
	}
	return true
}

func (f *fakeLLM) requestNotifyChan() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.requestNotify == nil {
		f.requestNotify = make(chan struct{}, 64)
	}
	return f.requestNotify
}

func (f *fakeLLM) notifyRequestLocked() {
	if f.requestNotify == nil {
		return
	}
	select {
	case f.requestNotify <- struct{}{}:
	default:
	}
}

func waitRequestCount(t *testing.T, f *fakeLLM, want int) {
	t.Helper()
	notify := f.requestNotifyChan()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		if f.requestCount() >= want {
			return
		}
		select {
		case <-notify:
		case <-deadline.C:
			t.Fatalf("request count = %d, want at least %d", f.requestCount(), want)
		}
	}
}

func newTestStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestTurnResponseTimeoutNotifiesUser(t *testing.T) {
	p := &fakePlatform{}
	block := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
	a := New(p, &fakeLLM{chatBlocks: []fakeLLMBlock{block}}, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.responseTimeout = 10 * time.Millisecond

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "本轮处理已超时停止") {
		t.Fatalf("platform output = %q, want timeout notice", got)
	}
}

func TestTurnResponseTimeoutZeroAllowsLongTurn(t *testing.T) {
	p := &fakePlatform{}
	block := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
	a := New(p, &fakeLLM{chatBlocks: []fakeLLMBlock{block}, replies: []string{"done"}}, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.responseTimeout = 0

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(context.Background(), "hello") }()
	<-block.started
	time.Sleep(20 * time.Millisecond)
	close(block.release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleMessage: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HandleMessage did not finish after release")
	}
	if got := p.out.String(); !strings.Contains(got, "done") {
		t.Fatalf("platform output = %q, want final reply", got)
	}
}

func TestHandleMessageSendsLLMErrorToPlatform(t *testing.T) {
	p := &fakePlatform{}
	a := New(p, &fakeLLM{replies: []string{"__ERR__"}}, "test-model", config.ProviderConfig{}, newTestStore(t))

	err := a.HandleMessage(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "fake stream error") {
		t.Fatalf("HandleMessage err = %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "LLM 响应中断：") || !strings.Contains(got, "fake stream error") {
		t.Fatalf("platform output missing error: %q", got)
	}
}

func TestHandleMessageImageOnlyInputReachesLLM(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"ok"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform: "qqofficial",
		ScopeID:  "c2c:user-1",
		Sender:   p,
		Segments: []platform.MessageSegment{{Type: platform.SegmentImage, URL: "data:image/png;base64,abc", MIMEType: "image/png", Name: "image.png"}},
	})

	if err := a.HandleMessage(ctx, ""); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d, want 1", len(requests))
	}
	latest := llm.LatestUserSegments(requests[0].Messages)
	if len(latest) != 1 || latest[0].Type != llm.SegmentImage {
		t.Fatalf("latest user segments = %#v, want image only", latest)
	}
}

func TestReplaceInboundTextSegmentsPreservesImage(t *testing.T) {
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Segments: []platform.MessageSegment{
		{Type: platform.SegmentText, Text: "@tool:web 看看"},
		{Type: platform.SegmentImage, URL: "data:image/png;base64,abc", MIMEType: "image/png"},
	}})

	segments := replaceInboundTextSegments(ctx, "看看")
	if len(segments) != 2 {
		t.Fatalf("segments len = %d, want 2", len(segments))
	}
	if segments[0].Type != llm.SegmentText || segments[0].Text != "看看" {
		t.Fatalf("text segment = %#v, want replaced text", segments[0])
	}
	if segments[1].Type != llm.SegmentImage || segments[1].URL == "" {
		t.Fatalf("image segment = %#v, want preserved image", segments[1])
	}
}

func TestPlatformMessageReceivedHookSendsOutputs(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "test.received.output", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = append(event.Outputs, delivery.Text("received output"))
		return event, nil
	})}); err != nil {
		t.Fatalf("Register received hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "received output") || !strings.Contains(got, "final") {
		t.Fatalf("platform output = %q", got)
	}
}

func TestUnwokenGroupMessageSkipsLLMButAllowsPassiveHook(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	allowPassive := false
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "test.passive", Match: hook.Always(), RequireWakeup: &allowPassive, Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = append(event.Outputs, delivery.Text("passive output"))
		return event, nil
	})}); err != nil {
		t.Fatalf("Register passive hook: %v", err)
	}
	a.SetHookManager(manager)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "hello",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "hello"}},
	})

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := p.out.String(); !strings.Contains(got, "passive output") || strings.Contains(got, "final") {
		t.Fatalf("platform output = %q", got)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests = %d, want 0", got)
	}
}

func TestUnwokenGroupMessageSkipsDefaultHook(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "test.default", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = append(event.Outputs, delivery.Text("default output"))
		return event, nil
	})}); err != nil {
		t.Fatalf("Register default hook: %v", err)
	}
	a.SetHookManager(manager)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "hello",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "hello"}},
	})

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := p.out.String(); strings.Contains(got, "default output") || strings.Contains(got, "final") {
		t.Fatalf("platform output = %q", got)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests = %d, want 0", got)
	}
}

func TestPassiveHookCannotWakeLLMByEditingMessage(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	allowPassive := false
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "test.edit", Match: hook.Always(), RequireWakeup: &allowPassive, Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Message.Segments = llm.TextSegments("芙莉丝 hello")
		return event, nil
	})}); err != nil {
		t.Fatalf("Register passive hook: %v", err)
	}
	a.SetHookManager(manager)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "hello",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "hello"}},
		TriggerKeywords:  []string{"芙莉丝"},
	})

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests = %d, want 0", got)
	}
}

func TestWokenGroupMessageStripsTriggerKeywordBeforeLLM(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "芙莉丝 hello",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "芙莉丝 hello"}},
		TriggerKeywords:  []string{"芙莉丝"},
	})

	if err := a.HandleMessage(ctx, "芙莉丝 hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d, want 1", len(requests))
	}
	if got := llm.LatestUserSegmentTextOnly(requests[0].Messages); got != "hello" {
		t.Fatalf("latest user text = %q", got)
	}
}

func TestPlatformMessageReceivedHookMatchesCurrentTextWithReplyContext(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	allowPassive := false
	if err := manager.Register(hook.Registration{
		Point:         hook.PointPlatformMessageReceived,
		Name:          "test.recall.reply",
		RequireWakeup: &allowPassive,
		Match: hook.Match{Conditions: []hook.Condition{
			{Field: "message.text", Op: hook.MatchFull, Value: "撤回"},
			{Field: "message.reply.message_id", Op: hook.MatchExists},
		}},
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
			if event.Message.PlatformText != "撤回" {
				t.Fatalf("platform text = %q, want current text", event.Message.PlatformText)
			}
			if event.Message.Reply == nil || event.Message.Reply.Text != "通知内容" {
				t.Fatalf("reply = %#v, want structured reply", event.Message.Reply)
			}
			event.Outputs = append(event.Outputs, delivery.Text("recalled"))
			event.Control.Consume = true
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register recall hook: %v", err)
	}
	a.SetHookManager(manager)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "撤回",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "撤回"}},
		ContextText:      "[引用：通知]：通知内容\n\n撤回",
		ContextSegments:  []platform.MessageSegment{{Type: platform.SegmentText, Text: "[引用：通知]：通知内容\n\n撤回"}},
		Reply:            platform.ReplyContext{MessageID: "notice-1", SenderID: "bot", Text: "通知内容", Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: "通知内容"}}},
	})

	if err := a.HandleMessage(ctx, "撤回"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := p.out.String(); got != "recalled" {
		t.Fatalf("platform output = %q, want recalled", got)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests = %d, want 0", got)
	}
}

func TestReplyContextFallbackStillReachesLLMWhenNotConsumed(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "芙莉丝 继续",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "芙莉丝 继续"}},
		ContextText:      "[引用：通知]：通知内容\n\n芙莉丝 继续",
		ContextSegments:  []platform.MessageSegment{{Type: platform.SegmentText, Text: "[引用：通知]：通知内容\n\n芙莉丝 继续"}},
		Reply:            platform.ReplyContext{MessageID: "notice-1", Text: "通知内容", Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: "通知内容"}}},
		TriggerKeywords:  []string{"芙莉丝"},
	})

	if err := a.HandleMessage(ctx, "芙莉丝 继续"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d, want 1", len(requests))
	}
	if got := llm.LatestUserSegmentTextOnly(requests[0].Messages); got != "[引用：通知]：通知内容\n\n继续" {
		t.Fatalf("latest user text = %q", got)
	}
}

func TestPlatformMessageReceivedHookConsumeSkipsCommandAndLLM(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "test.received.consume", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = append(event.Outputs, delivery.Text("consumed"))
		event.Control.Consume = true
		return event, nil
	})}); err != nil {
		t.Fatalf("Register received hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "/help"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "consumed") {
		t.Fatalf("platform output = %q", got)
	}
	if strings.Contains(got, "可用命令") || strings.Contains(got, "final") {
		t.Fatalf("consume should skip command and LLM, output = %q", got)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests = %d, want 0", got)
	}
}

func TestHandleMessageSendsFallbackForEmptyLLMResponse(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq-onebot", ScopeID: "private:test", Sender: p, BufferAssistantOutput: true})

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := p.out.String(); !strings.Contains(got, "模型这次没有返回可见内容") {
		t.Fatalf("platform output missing empty response fallback: %q", got)
	}
	if got := len(f.chatRequests()); got != 1 {
		t.Fatalf("chat requests = %d, want 1", got)
	}
}

func TestStreamingOutputAppendsRawAndReplacesHookText(t *testing.T) {
	p := &fakeStreamingPlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{
		{DeltaContent: "hello "},
		{DeltaContent: "[[wave]]"},
	}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMResponseReceived, Name: "test.replace", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.LLM.Text = strings.ReplaceAll(event.LLM.Text, "[[wave]]", "world")
		return event, nil
	})}); err != nil {
		t.Fatalf("Register response hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := strings.Join(p.stream.appends, ""); got != "hello [[wave]]" {
		t.Fatalf("stream appends = %q", got)
	}
	if len(p.stream.replaces) != 1 || p.stream.replaces[0] != "hello world" {
		t.Fatalf("stream replaces = %#v", p.stream.replaces)
	}
	if p.stream.finished != 1 {
		t.Fatalf("stream finished = %d", p.stream.finished)
	}
	if strings.Contains(p.out.String(), "hello world") {
		t.Fatalf("streaming output should not also send normal chat: %q", p.out.String())
	}
}

func TestStreamingOutputPreparedHookReplacesFinalMessage(t *testing.T) {
	p := &fakeStreamingPlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: "猫"}}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointAgentOutputPrepared, Name: "test.output", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Message.Segments = llm.ReplaceSegmentText(event.Message.Segments, regexp.MustCompile("猫"), "狗", true)
		return event, nil
	})}); err != nil {
		t.Fatalf("Register output hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := strings.Join(p.stream.appends, ""); got != "猫" {
		t.Fatalf("stream appends = %q", got)
	}
	if len(p.stream.replaces) != 1 || p.stream.replaces[0] != "狗" {
		t.Fatalf("stream replaces = %#v", p.stream.replaces)
	}
}

func TestTurnOutputPreparedHookReplacesFinalStreamingMessage(t *testing.T) {
	p := &fakeStreamingPlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: "猫"}}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointAgentTurnOutputPrepared, Name: "test.turn_output", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Message.Segments = llm.ReplaceSegmentText(event.Message.Segments, regexp.MustCompile("猫"), "狗", true)
		return event, nil
	})}); err != nil {
		t.Fatalf("Register turn output hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := strings.Join(p.stream.appends, ""); got != "猫" {
		t.Fatalf("stream appends = %q", got)
	}
	if len(p.stream.replaces) != 1 || p.stream.replaces[0] != "狗" {
		t.Fatalf("stream replaces = %#v", p.stream.replaces)
	}
}

func TestNonStreamingPlatformSendsOnlyHookText(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{
		{DeltaContent: "hello "},
		{DeltaContent: "[[wave]]"},
	}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMResponseReceived, Name: "test.replace", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.LLM.Text = strings.ReplaceAll(event.LLM.Text, "[[wave]]", "world")
		return event, nil
	})}); err != nil {
		t.Fatalf("Register response hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "hello world") || strings.Contains(got, "[[wave]]") {
		t.Fatalf("non-streaming output = %q", got)
	}
}

func TestAfterAssistantOutputsAreSentAfterFinalText(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final text"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMResponseReceived, Name: "test.outputs", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = append(event.Outputs,
			delivery.Text("immediate"),
			delivery.WithDeliveryTiming(delivery.Text("after"), delivery.DeliveryAfterAssistant),
		)
		return event, nil
	})}); err != nil {
		t.Fatalf("Register response hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := p.out.String()
	immediateIdx := strings.Index(got, "immediate")
	finalIdx := strings.Index(got, "final text")
	afterIdx := strings.Index(got, "after")
	if immediateIdx < 0 || finalIdx < 0 || afterIdx < 0 || !(immediateIdx < finalIdx && finalIdx < afterIdx) {
		t.Fatalf("output = %q, want immediate before final text before after", got)
	}
}

func TestCompleteForkMessageID(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{}
	store := newTestStore(t)
	a := New(p, f, "m", config.ProviderConfig{}, store)
	ctx := context.Background()

	session, err := a.sessions.Create(ctx, a.scope(context.Background()), "completion")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	user := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "question"}
	assistant := &storage.Message{ID: "abcdef-message", SessionID: session.ID, Role: storage.RoleAssistant, Content: "answer"}
	for _, message := range []*storage.Message{user, assistant} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}

	got := a.Complete("/fork abc")
	if len(got) != 1 || got[0] != "/fork abcdef-message" {
		t.Fatalf("Complete = %#v", got)
	}
	if got := a.Complete("/fork no-match"); len(got) != 0 {
		t.Fatalf("Complete no-match = %#v", got)
	}
}
func TestCheckModelMarksCurrent(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{models: []string{"beta", "alpha"}}
	a := New(p, f, "beta", config.ProviderConfig{}, newTestStore(t))

	if err := a.HandleMessage(context.Background(), "/checkmodel"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	got := p.out.String()
	if !strings.Contains(got, "default:\n") || !strings.Contains(got, "* [2] beta (chat, work, elwisp1, elwisp2, elwisp3, compact)") {
		t.Fatalf("model marker missing from output:\n%s", got)
	}
}

func TestModelsGroupsProvidersAndSwitchPersistsState(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	statePath := filepath.Join(t.TempDir(), "state.toml")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"glm-4-flash"}]}`)
	}))
	defer srv.Close()
	providers := map[string]config.ProviderConfig{
		"deepseek": {Models: []string{"deepseek-chat"}},
		"zhipu":    {BaseURL: srv.URL, APIKey: "test-key", Models: []string{"glm-4-flash"}},
	}
	f := &fakeLLM{models: []string{"deepseek-chat"}}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "deepseek", Model: "deepseek-chat"},
		storage.SessionModeChat: {Provider: "zhipu", Model: "glm-4-flash"},
	}
	a := NewWithOptions(p, f, "deepseek", modeModels, providers, statePath, store, []string{"/"}, session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}, config.ModelSelection{}, nil, "", nil, "")

	if err := a.HandleMessage(context.Background(), "/models zhipu"); err != nil {
		t.Fatalf("models: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "zhipu:\n") || !strings.Contains(got, "[2] glm-4-flash") {
		t.Fatalf("missing provider grouped model output: %q", got)
	}

	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/model 2"); err != nil {
		t.Fatalf("model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched to model: zhipu/glm-4-flash") {
		t.Fatalf("unexpected switch output: %q", p.out.String())
	}
	if a.CurrentProvider() != "zhipu" || a.CurrentModel() != "glm-4-flash" {
		t.Fatalf("current model = %s/%s", a.CurrentProvider(), a.CurrentModel())
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	state := string(data)
	if !strings.Contains(state, `[mode_models.work]`) || !strings.Contains(state, `provider = 'zhipu'`) || !strings.Contains(state, `model = 'glm-4-flash'`) {
		t.Fatalf("state not persisted: %s", state)
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/model --chat 1"); err != nil {
		t.Fatalf("chat model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched chat model: deepseek/deepseek-chat") {
		t.Fatalf("unexpected chat switch output: %q", p.out.String())
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/model --work 2"); err != nil {
		t.Fatalf("work model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched work model: zhipu/glm-4-flash") {
		t.Fatalf("unexpected work switch output: %q", p.out.String())
	}
	data, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read mode state: %v", err)
	}
	state = string(data)
	if !strings.Contains(state, `[mode_models.chat]`) || !strings.Contains(state, `[mode_models.work]`) || !strings.Contains(state, `provider = 'deepseek'`) || !strings.Contains(state, `provider = 'zhipu'`) {
		t.Fatalf("mode state not persisted: %s", state)
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/models"); err != nil {
		t.Fatalf("models after mode switches: %v", err)
	}
	modelsOut := p.out.String()
	if !strings.Contains(modelsOut, "deepseek-chat (chat") || !strings.Contains(modelsOut, "glm-4-flash (work") || strings.Contains(modelsOut, "current") {
		t.Fatalf("models missing mode markers: %q", modelsOut)
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/model --compact 1"); err != nil {
		t.Fatalf("compact model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched compact model: deepseek/deepseek-chat") {
		t.Fatalf("unexpected compact switch output: %q", p.out.String())
	}
	data, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read compact state: %v", err)
	}
	state = string(data)
	if !strings.Contains(state, `[compact_model]`) || !strings.Contains(state, `model = 'deepseek-chat'`) {
		t.Fatalf("compact state not persisted: %s", state)
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/model --naming 1"); err != nil {
		t.Fatalf("naming model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched naming model: deepseek/deepseek-chat") {
		t.Fatalf("unexpected naming switch output: %q", p.out.String())
	}
	data, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read naming state: %v", err)
	}
	state = string(data)
	if !strings.Contains(state, `[naming_model]`) || !strings.Contains(state, `model = 'deepseek-chat'`) {
		t.Fatalf("naming state not persisted: %s", state)
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/models deepseek"); err != nil {
		t.Fatalf("models after naming: %v", err)
	}
	if !strings.Contains(p.out.String(), "naming") {
		t.Fatalf("models missing naming marker: %q", p.out.String())
	}
}

func TestModelsShowsMissingAPIKeyEnv(t *testing.T) {
	p := &fakePlatform{}
	providers := map[string]config.ProviderConfig{
		"local":  {Models: []string{"local-model"}},
		"openai": {BaseURL: "https://example.invalid/v1", APIKeyEnv: "OPENAI_API_KEY", Models: []string{"configured-openai"}},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "local", Model: "local-model"},
		storage.SessionModeChat: {Provider: "local", Model: "local-model"},
	}
	a := NewWithOptions(p, &fakeLLM{models: []string{"local-model"}}, "local", modeModels, providers, "", newTestStore(t), []string{"/"}, session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}, config.ModelSelection{}, nil, "", nil, "")

	if err := a.HandleMessage(context.Background(), "/models"); err != nil {
		t.Fatalf("models: %v", err)
	}
	got := p.out.String()
	for _, want := range []string{"local-model", "configured-openai", "model provider errors:", `openai: api_key_env "OPENAI_API_KEY" is not set`} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestModelsShowsProviderFetchErrorsAndHealthyModels(t *testing.T) {
	p := &fakePlatform{}
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"ok-remote"}]}`)
	}))
	defer okSrv.Close()
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad api key"}}`, http.StatusUnauthorized)
	}))
	defer badSrv.Close()

	providers := map[string]config.ProviderConfig{
		"bad":   {BaseURL: badSrv.URL, APIKey: "wrong-key", Models: []string{"bad-configured"}},
		"local": {Models: []string{"local-model"}},
		"ok":    {BaseURL: okSrv.URL, APIKey: "ok-key", Models: []string{"ok-configured"}},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "local", Model: "local-model"},
		storage.SessionModeChat: {Provider: "local", Model: "local-model"},
	}
	a := NewWithOptions(p, &fakeLLM{models: []string{"local-model"}}, "local", modeModels, providers, "", newTestStore(t), []string{"/"}, session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}, config.ModelSelection{}, nil, "", nil, "")

	if err := a.HandleMessage(context.Background(), "/models"); err != nil {
		t.Fatalf("models: %v", err)
	}
	got := p.out.String()
	for _, want := range []string{"ok:\n", "ok-configured", "ok-remote", "bad:\n", "bad-configured", "model provider errors:", "bad:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(strings.ToLower(got), "bad api key") && !strings.Contains(got, "401") {
		t.Fatalf("output missing provider error detail:\n%s", got)
	}
}

func TestModelOptionsFetchesProvidersInParallel(t *testing.T) {
	newModelServer := func(model string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(150 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, model)
		}))
	}
	srvA := newModelServer("remote-a")
	defer srvA.Close()
	srvB := newModelServer("remote-b")
	defer srvB.Close()

	providers := map[string]config.ProviderConfig{
		"local": {Models: []string{"local-model"}},
		"a":     {BaseURL: srvA.URL, APIKey: "test-key", Models: []string{"configured-a"}},
		"b":     {BaseURL: srvB.URL, APIKey: "test-key", Models: []string{"configured-b"}},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "local", Model: "local-model"},
		storage.SessionModeChat: {Provider: "local", Model: "local-model"},
	}
	a := NewWithOptions(&fakePlatform{}, &fakeLLM{models: []string{"local-model"}}, "local", modeModels, providers, "", newTestStore(t), []string{"/"}, session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}, config.ModelSelection{}, nil, "", nil, "")

	startedAt := time.Now()
	options := a.Models("")
	elapsed := time.Since(startedAt)

	if elapsed >= 250*time.Millisecond {
		t.Fatalf("model provider fetches appear serial, elapsed=%s", elapsed)
	}
	got := []string{}
	for _, option := range options {
		got = append(got, option.Provider+"/"+option.Model)
	}
	want := []string{"a/configured-a", "a/remote-a", "b/configured-b", "b/remote-b", "local/local-model"}
	if !containsAll(got, want) {
		t.Fatalf("models = %#v, want to contain %#v", got, want)
	}
}

func TestModelOptionsCachesProviderModelsUntilFresh(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"id":"remote-%d"}]}`, requests)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"local":  {Models: []string{"local-model"}},
		"remote": {BaseURL: srv.URL, APIKey: "test-key"},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "local", Model: "local-model"},
		storage.SessionModeChat: {Provider: "local", Model: "local-model"},
	}
	a := NewWithOptions(&fakePlatform{}, &fakeLLM{models: []string{"local-model"}}, "local", modeModels, providers, "", newTestStore(t), []string{"/"}, session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}, config.ModelSelection{}, nil, "", nil, "")

	first := a.ModelList("", agentcommands.ModelListOptions{})
	second := a.ModelList("", agentcommands.ModelListOptions{})
	fresh := a.ModelList("", agentcommands.ModelListOptions{Fresh: true})

	if requests != 2 {
		t.Fatalf("provider model requests = %d, want 2", requests)
	}
	if first.Options[0].Model != "local-model" || second.Options[0].Model != "local-model" || fresh.Options[0].Model != "local-model" {
		t.Fatalf("unexpected cached models: first=%#v second=%#v fresh=%#v", first.Options, second.Options, fresh.Options)
	}
}

func TestDynamicProviderClientUsesAgentLogger(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	var logs bytes.Buffer
	providers := map[string]config.ProviderConfig{
		"deepseek": {Models: []string{"deepseek-chat"}},
		"zhipu":    {BaseURL: srv.URL, APIKey: "secret-key", Models: []string{"glm-4-flash"}},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "deepseek", Model: "deepseek-chat"},
		storage.SessionModeChat: {Provider: "zhipu", Model: "glm-4-flash"},
	}
	a := NewWithOptions(&fakePlatform{}, &fakeLLM{}, "deepseek", modeModels, providers, "", newTestStore(t), []string{"/"}, session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}, config.ModelSelection{}, nil, "", nil, "")
	a.SetLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	ch, err := a.clientForProvider("zhipu").ChatStream(context.Background(), llm.ChatRequest{
		Model:    "glm-4-flash",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("动态 provider 请求")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	for range ch {
	}

	logText := logs.String()
	if !strings.Contains(logText, "openai chat request") || !strings.Contains(logText, "动态 provider 请求") {
		t.Fatalf("dynamic provider request was not logged: %s", logText)
	}
	if strings.Contains(logText, "secret-key") || strings.Contains(logText, "Authorization") {
		t.Fatalf("debug log leaked credentials: %s", logText)
	}
	if len(capturedBody) == 0 {
		t.Fatal("server did not receive request body")
	}
}

func TestMapSentAssistantMessageMapsAllReceiptIDs(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qqonebot", PlatformUserID: "1", ScopeID: "group:9"})
	session, err := a.sessions.Create(ctx, a.scope(ctx), "mapped")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	assistant := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "long answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	a.mapSentAssistantMessage(ctx, session.ID, assistant.ID, delivery.Receipt{PlatformMessageIDs: []string{"101", "", "102"}})
	for _, platformMessageID := range []string{"101", "102"} {
		got, err := store.Messages().FindByPlatformMessage(ctx, "qqonebot", "group:9", platformMessageID)
		if err != nil {
			t.Fatalf("find platform message %s: %v", platformMessageID, err)
		}
		if got.ID != assistant.ID {
			t.Fatalf("platform message %s mapped to %s, want %s", platformMessageID, got.ID, assistant.ID)
		}
	}
}

func TestModelSwitchUsesMessagePlatformCurrentModeForGlobalState(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	statePath := filepath.Join(t.TempDir(), "state.toml")
	providers := map[string]config.ProviderConfig{
		"deepseek": {Models: []string{"deepseek-chat"}},
		"zhipu":    {Models: []string{"glm-4-flash"}},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "deepseek", Model: "deepseek-chat"},
		storage.SessionModeChat: {Provider: "deepseek", Model: "deepseek-chat"},
	}
	a := NewWithOptions(p, &fakeLLM{models: []string{"deepseek-chat"}}, "deepseek", modeModels, providers, statePath, store, []string{"/"}, session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}, config.ModelSelection{}, nil, "", nil, "")
	a.RegisterPlatformSender("qq", p)
	qqCtx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "admin", ScopeID: "group:9"})
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"qq": {"admin"}}))

	qqSession, err := a.sessions.Create(qqCtx, a.scope(qqCtx), "qq chat")
	if err != nil {
		t.Fatalf("create qq session: %v", err)
	}
	qqSession.Mode = storage.SessionModeChat
	if err := store.Sessions().Update(qqCtx, qqSession); err != nil {
		t.Fatalf("update qq session mode: %v", err)
	}

	if err := a.HandleMessage(qqCtx, "/model zhipu/glm-4-flash"); err != nil {
		t.Fatalf("model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched to model: zhipu/glm-4-flash") {
		t.Fatalf("unexpected switch output: %q", p.out.String())
	}
	if got := a.CurrentModelForMode(storage.SessionModeChat); got.Provider != "zhipu" || got.Model != "glm-4-flash" {
		t.Fatalf("chat model = %#v", got)
	}
	if got := a.CurrentModelForMode(storage.SessionModeWork); got.Provider != "deepseek" || got.Model != "deepseek-chat" {
		t.Fatalf("work model = %#v", got)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	state := string(data)
	if !strings.Contains(state, `[mode_models.chat]`) || !strings.Contains(state, `provider = 'zhipu'`) || !strings.Contains(state, `model = 'glm-4-flash'`) {
		t.Fatalf("chat model not persisted globally: %s", state)
	}
	if !strings.Contains(state, `[mode_models.work]`) || !strings.Contains(state, `provider = 'deepseek'`) || !strings.Contains(state, `model = 'deepseek-chat'`) {
		t.Fatalf("work model should stay unchanged: %s", state)
	}
}

func TestUnknownCommandDoesNotCallLLM(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))

	if err := a.HandleMessage(context.Background(), "/doesnotexist"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if f.requestCount() != 0 {
		t.Fatalf("LLM was called for unknown command")
	}
	if !strings.Contains(p.out.String(), "unknown command: /doesnotexist") {
		t.Fatalf("unexpected output: %q", p.out.String())
	}
}

func TestConfiguredCommandPrefixAlias(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{models: []string{"alpha"}}
	a := NewWithPrefixes(p, f, map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "default", Model: "alpha"},
		storage.SessionModeChat: {Provider: "default", Model: "alpha"},
	}, config.ProviderConfig{}, newTestStore(t), []string{"/", "-"})

	if err := a.HandleMessage(context.Background(), "-help"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if f.requestCount() != 0 {
		t.Fatalf("LLM was called for alias command")
	}
	if !strings.Contains(p.out.String(), "available commands:") || !strings.Contains(p.out.String(), "-help") {
		t.Fatalf("unexpected help output: %q", p.out.String())
	}
}

func TestSessionIdleExpiration(t *testing.T) {
	defaultIdleExpiration := config.SessionIdleExpirationConfig{
		GroupUserTTLMinutes:         10,
		GroupSuperadminTTLMinutes:   10,
		PrivateUserTTLMinutes:       10,
		PrivateSuperadminTTLMinutes: 0,
	}
	tests := []struct {
		name        string
		ctx         platform.MessageContext
		superadmin  bool
		cfg         config.SessionIdleExpirationConfig
		wantExpired bool
	}{
		{name: "group user expires", ctx: platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"}, cfg: defaultIdleExpiration, wantExpired: true},
		{name: "group superadmin expires", ctx: platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"}, superadmin: true, cfg: defaultIdleExpiration, wantExpired: true},
		{name: "private user expires", ctx: platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "private:1"}, cfg: defaultIdleExpiration, wantExpired: true},
		{name: "private superadmin keeps", ctx: platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "private:1"}, superadmin: true, cfg: defaultIdleExpiration, wantExpired: false},
		{name: "disabled group user keeps", ctx: platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"}, cfg: config.SessionIdleExpirationConfig{GroupUserTTLMinutes: 0, GroupSuperadminTTLMinutes: 10, PrivateUserTTLMinutes: 10}, wantExpired: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakePlatform{}
			store := newTestStore(t)
			f := &fakeLLM{replies: []string{"fresh reply"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, store)
			if tt.superadmin {
				a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"qq": {"1"}}))
			}
			a.SetSessionIdleExpiration(tt.cfg)
			ctx := platform.WithMessageContext(context.Background(), tt.ctx)

			oldSession, err := a.sessions.Create(ctx, a.scope(ctx), "old")
			if err != nil {
				t.Fatalf("create old session: %v", err)
			}
			oldSession.UpdatedAt = time.Now().Add(-11 * time.Minute)
			if err := store.Sessions().Update(ctx, oldSession); err != nil {
				t.Fatalf("age old session: %v", err)
			}

			if err := a.HandleMessage(ctx, "hello again"); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			_, oldErr := store.Sessions().Get(ctx, oldSession.ID)
			current, err := a.sessions.Current(ctx, a.scope(ctx))
			if err != nil {
				t.Fatalf("current session: %v", err)
			}
			if tt.wantExpired {
				if !errors.Is(oldErr, storage.ErrNotFound) {
					t.Fatalf("old session err = %v, want not found", oldErr)
				}
				if current.ID == oldSession.ID {
					t.Fatal("current session was not replaced")
				}
			} else {
				if oldErr != nil {
					t.Fatalf("old session err = %v, want nil", oldErr)
				}
				if current.ID != oldSession.ID {
					t.Fatalf("current session = %s, want %s", current.ID, oldSession.ID)
				}
			}
		})
	}
}

func TestMessageContextResumeStartsTargetSession(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"resume reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"})

	bg := &storage.Session{OwnerID: "qq:1", Platform: "qq", PlatformScopeID: "cron:user.cron.test", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "cron"}
	if err := store.Sessions().Create(ctx, bg); err != nil {
		t.Fatalf("create background session: %v", err)
	}
	resumeCtx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9", ResumeSessionID: bg.ID})
	if err := a.HandleMessage(resumeCtx, "continue here"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	current, err := a.sessions.Current(resumeCtx, a.scope(resumeCtx))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if current.ID != bg.ID {
		t.Fatalf("current = %s, want %s", current.ID, bg.ID)
	}
	messages, err := store.Messages().ListBySession(resumeCtx, bg.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Content != "continue here" || messages[1].Content != "resume reply" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestMessageContextForkStartsForkSession(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"fork reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"})

	source, err := a.sessions.Create(ctx, a.scope(ctx), "source")
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	assistant := &storage.Message{SessionID: source.ID, Role: storage.RoleAssistant, Content: "answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	forkCtx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9", ForkFromMessageID: assistant.ID})
	if err := a.HandleMessage(forkCtx, "continue from here"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	current, err := a.sessions.Current(forkCtx, a.scope(forkCtx))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if current.ID == source.ID || current.ParentSessionID != source.ID || current.ForkFromMessageID != assistant.ID {
		t.Fatalf("fork session = %#v, source = %s assistant = %s", current, source.ID, assistant.ID)
	}
	messages, err := store.Messages().ListBySession(forkCtx, current.ID)
	if err != nil {
		t.Fatalf("list fork messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Content != "continue from here" || messages[1].Content != "fork reply" {
		t.Fatalf("fork messages = %#v", messages)
	}
}

func TestChatPersistsMessagesAndLoadsHistory(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"hi", "again"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("first HandleMessage: %v", err)
	}
	if err := a.HandleMessage(ctx, "second"); err != nil {
		t.Fatalf("second HandleMessage: %v", err)
	}

	sessions, err := store.Sessions().List(ctx, storage.ListSessionsRequest{
		ActorID:         "cli:local",
		Platform:        p.Name(),
		PlatformScopeID: "local",
	})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d", len(sessions))
	}

	messages, err := store.Messages().ListBySession(ctx, sessions[0].ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("message count = %d, messages = %#v", len(messages), messages)
	}
	if messages[0].Role != storage.RoleUser || messages[0].Content != "hello" {
		t.Fatalf("first message = %#v", messages[0])
	}
	if messages[1].Role != storage.RoleAssistant || messages[1].Content != "hi" {
		t.Fatalf("second message = %#v", messages[1])
	}
	if messages[2].Role != storage.RoleUser || messages[2].Content != "second" {
		t.Fatalf("third message = %#v", messages[2])
	}
	if messages[3].Role != storage.RoleAssistant || messages[3].Content != "again" {
		t.Fatalf("fourth message = %#v", messages[3])
	}

	chatRequests := f.chatRequests()
	if len(chatRequests) != 2 {
		t.Fatalf("chat request count = %d", len(chatRequests))
	}
	secondReq := chatRequests[1]
	if len(secondReq.Messages) != 4 {
		t.Fatalf("second request messages = %#v", secondReq.Messages)
	}
	if secondReq.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("missing system prompt: %#v", secondReq.Messages)
	}
	if llm.SegmentsContentText(secondReq.Messages[1].Segments) != "hello" || llm.SegmentsContentText(secondReq.Messages[2].Segments) != "hi" || llm.SegmentsContentText(secondReq.Messages[3].Segments) != "second" {
		t.Fatalf("history not loaded: %#v", secondReq.Messages)
	}
}

func TestChatSchedulesAsyncNamingAndSessionsShowPreview(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"main reply"}, titleReplies: []string{"generated title"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "hello naming"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	waitRequestCount(t, f, 2)

	sessions, err := store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d", len(sessions))
	}
	deadline := time.Now().Add(time.Second)
	for sessions[0].Title != "generated title" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		sessions, err = store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
		if err != nil {
			t.Fatalf("list sessions: %v", err)
		}
	}
	if sessions[0].Title != "generated title" {
		t.Fatalf("title = %q", sessions[0].Title)
	}

	if err := a.HandleMessage(ctx, "/sessions"); err != nil {
		t.Fatalf("sessions command: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "preview: u: hello naming / b: main reply") {
		t.Fatalf("missing preview output: %q", got)
	}
}

func TestChatFailureDoesNotScheduleNaming(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"__ERR__"}, titleReplies: []string{"should not be used"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)

	if err := a.HandleMessage(context.Background(), "hello naming"); err == nil {
		t.Fatal("expected chat error")
	}
	time.Sleep(50 * time.Millisecond)
	if f.requestCount() != 1 {
		t.Fatalf("request count = %d, want only the failed chat request", f.requestCount())
	}
}

func TestChatExecutesToolAndFollowsUp(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "目录已查看"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	if err := registry.Register(tool.NewDiscoverTool(registry)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(newAgentShellTool()); err != nil {
		t.Fatal(err)
	}
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "看看目录"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	preview := p.preview.String()
	if strings.Contains(preview, "模型准备调用工具：shell") {
		t.Fatalf("unexpected preparation preview output: %q", preview)
	}
	if !strings.Contains(preview, "正在调用 shell") {
		t.Fatalf("missing preview output: %q", preview)
	}
	if strings.Contains(preview, "shell 调用完成") {
		t.Fatalf("unexpected success preview output: %q", preview)
	}
	if !strings.Contains(p.out.String(), "目录已查看") {
		t.Fatalf("missing final answer: %q", p.out.String())
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	followup := requests[1]
	if len(followup.Messages) < 2 {
		t.Fatalf("followup messages = %#v", followup.Messages)
	}
	last := followup.Messages[len(followup.Messages)-1]
	if last.Role != llm.RoleTool || last.ToolCallID != "call_1" || last.Name != "shell" || !strings.Contains(llm.SegmentsContentText(last.Segments), "stdout") {
		t.Fatalf("missing tool result message: %#v", last)
	}

	sessions, err := store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.Messages().ListBySession(ctx, sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[1].Role != storage.RoleAssistant || messages[2].Role != storage.RoleTool || messages[3].Content != "目录已查看" {
		t.Fatalf("messages = %#v", messages)
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, "/status"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(p.out.String(), "tool usage: shell x1") {
		t.Fatalf("status missing tool usage: %q", p.out.String())
	}

	resumedAgent := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
	if err := resumedAgent.HandleMessage(ctx, "/resume "+sessions[0].ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	p.out.Reset()
	if err := resumedAgent.HandleMessage(ctx, "/status"); err != nil {
		t.Fatalf("resumed status: %v", err)
	}
	if !strings.Contains(p.out.String(), "tool usage: shell x1") {
		t.Fatalf("resumed status missing tool usage: %q", p.out.String())
	}
}

func TestToolTranscriptPersistsWhenFollowupLLMFails(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"echo saved"}`}}, FinishReason: "tool_calls"}},
		{{Error: fmt.Errorf("followup failed")}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	err := a.HandleMessage(ctx, "run and save")
	if err == nil || !strings.Contains(err.Error(), "followup failed") {
		t.Fatalf("HandleMessage err = %v", err)
	}
	sessions, err := store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %#v", sessions)
	}
	messages, err := store.Messages().ListBySession(ctx, sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].Role != storage.RoleUser || messages[0].Content != "run and save" {
		t.Fatalf("user message not persisted: %#v", messages[0])
	}
	if messages[1].Role != storage.RoleAssistant || !strings.Contains(messages[1].Metadata, "call_1") {
		t.Fatalf("assistant tool call not persisted: %#v", messages[1])
	}
	if messages[2].Role != storage.RoleTool || messages[2].ToolCallID != "call_1" || !strings.Contains(messages[2].Content, "stdout") {
		t.Fatalf("tool result not persisted: %#v", messages[2])
	}
}

func TestTurnHookDoesNotRepeatDuringToolFollowup(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"echo hi"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMTurnPrepared, Name: "test.turn", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.LLM.Messages = llm.AppendSystemSegmentText(event.LLM.Messages, "TURN_MEMORY")
		return event, nil
	})}); err != nil {
		t.Fatalf("Register turn hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "run tool"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	for i, req := range requests {
		system := firstRequestSystemText(req)
		if strings.Count(system, "TURN_MEMORY") != 1 {
			t.Fatalf("request %d system memory count = %d, system=%q", i, strings.Count(system, "TURN_MEMORY"), system)
		}
	}
}

func firstRequestSystemText(req llm.ChatRequest) string {
	for _, message := range req.Messages {
		if message.Role == llm.RoleSystem {
			return llm.SegmentsContentText(message.Segments)
		}
	}
	return ""
}

func chatRequestText(req llm.ChatRequest) string {
	parts := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		if text := llm.SegmentsContentText(message.Segments); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func onlySession(t *testing.T, store storage.Store, p *fakePlatform) *storage.Session {
	t.Helper()
	sessions, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil || len(sessions) != 1 {
		t.Fatalf("list sessions: %#v err=%v", sessions, err)
	}
	sessionRecord, err := store.Sessions().Get(context.Background(), sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return sessionRecord
}

func TestDiscoveredToolsAreInjectedIntoTopLevelTools(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "discover_tool", Args: `{"name":"web_search"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebSearchTool())
	_ = registry.Register(builtin.NewWebExtractTool())
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "测试工具"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool" {
		t.Fatalf("initial tools = %#v", toolNames(requests[0].Tools))
	}
	if toolNames(requests[1].Tools) != "discover_tool,web_extract,web_search" {
		t.Fatalf("discovered tools not stable: req2=%s", toolNames(requests[1].Tools))
	}
	latestToolContent := llm.SegmentsContentText(requests[1].Messages[len(requests[1].Messages)-1].Segments)
	if !strings.Contains(latestToolContent, "已发现工具：web_search") {
		t.Fatalf("discover tool result should summarize found tool: %s", latestToolContent)
	}
	if strings.Contains(latestToolContent, `"schema"`) {
		t.Fatalf("discover tool result should not repeat schema json: %s", latestToolContent)
	}
	sessions, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil || len(sessions) == 0 {
		t.Fatalf("list sessions: %#v err=%v", sessions, err)
	}
	sessionRecord, err := store.Sessions().Get(context.Background(), sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sessionRecord.Metadata, "web_search") || !strings.Contains(sessionRecord.Metadata, "web_extract") {
		t.Fatalf("session metadata should persist discovered tool names: %q", sessionRecord.Metadata)
	}

	resumedAgent := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
	resumedAgent.SetToolRuntime(registry, nil)
	resumed, err := resumedAgent.toolsForSession(context.Background(), sessionRecord)
	if err != nil {
		t.Fatal(err)
	}
	if toolNames(resumed) != "discover_tool,web_extract,web_search" {
		t.Fatalf("restored tools = %s", toolNames(resumed))
	}
}

func TestToolDirectiveInjectsAndStripsValidTools(t *testing.T) {
	for _, directive := range []string{"@tool:web_search", "@tool：web_search", "@t:web_search", "@t：web_search"} {
		t.Run(directive, func(t *testing.T) {
			p := &fakePlatform{}
			store := newTestStore(t)
			f := &fakeLLM{replies: []string{"done"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, store)
			a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
			registry := tool.NewRegistry()
			_ = registry.Register(tool.NewDiscoverTool(registry))
			_ = registry.Register(builtin.NewWebSearchTool())
			_ = registry.Register(builtin.NewWebExtractTool())
			a.SetToolRuntime(registry, nil)

			if err := a.HandleMessage(context.Background(), "查资料 "+directive); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			requests := f.chatRequests()
			if len(requests) != 1 {
				t.Fatalf("chat requests = %d", len(requests))
			}
			if toolNames(requests[0].Tools) != "discover_tool,web_extract,web_search" {
				t.Fatalf("tools = %s", toolNames(requests[0].Tools))
			}
			latest := requests[0].Messages[len(requests[0].Messages)-1]
			if got := llm.SegmentsContentText(latest.Segments); got != "查资料" {
				t.Fatalf("latest user content = %q", got)
			}
			sessions, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
			if err != nil || len(sessions) == 0 {
				t.Fatalf("list sessions: %#v err=%v", sessions, err)
			}
			messages, err := store.Messages().ListBySession(context.Background(), sessions[0].ID)
			if err != nil {
				t.Fatalf("list messages: %v", err)
			}
			if len(messages) == 0 || messages[0].Content != "查资料" || strings.Contains(messages[0].Content, directive) {
				t.Fatalf("stored user message = %#v", messages)
			}
		})
	}
}

func TestToolDirectiveInjectsTaggedTools(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha", tags: []string{"web"}})
	_ = registry.Register(agentWrapperTool{name: "beta", tags: []string{"web"}})
	_ = registry.Register(agentDetailTool{name: "skill_web", source: tool.SourceSkillAgent, detail: "# skill", activate: []string{"python_skill_run"}})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "查资料 @tool:web"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool,alpha,beta" {
		t.Fatalf("tools = %s", toolNames(requests[0].Tools))
	}
}

func TestToolDirectiveInjectsConfiguredTagPrompt(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha"})
	a.SetToolRuntime(registry, nil)
	a.SetToolTagConfig("", config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{
		"worker": {Tools: []string{"alpha"}, Prompt: "Use alpha carefully."},
	}})

	if err := a.HandleMessage(context.Background(), "处理 @tool:worker"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool,alpha" {
		t.Fatalf("tools = %s", toolNames(requests[0].Tools))
	}
	system := llm.SegmentsContentText(requests[0].Messages[0].Segments)
	if !strings.Contains(system, "Use alpha carefully.") {
		t.Fatalf("missing tag prompt: %q", system)
	}
	if strings.Contains(system, "worker") {
		t.Fatalf("tag name leaked into system prompt: %q", system)
	}
	sessions, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil || len(sessions) == 0 {
		t.Fatalf("list sessions: %#v err=%v", sessions, err)
	}
	sessionRecord, err := store.Sessions().Get(context.Background(), sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	metadata := decodeSessionMetadata(sessionRecord.Metadata)
	if len(metadata.ToolTags) != 1 || metadata.ToolTags[0] != "worker" {
		t.Fatalf("tool tags metadata = %#v", metadata.ToolTags)
	}
}

func TestToolDirectiveDirectToolDoesNotActivateConfiguredTagPrompt(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha"})
	a.SetToolRuntime(registry, nil)
	a.SetToolTagConfig("", config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{
		"worker": {Tools: []string{"alpha"}, Prompt: "Use alpha carefully."},
	}})

	if err := a.HandleMessage(context.Background(), "处理 @tool:alpha"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	system := llm.SegmentsContentText(requests[0].Messages[0].Segments)
	if strings.Contains(system, "Use alpha carefully.") {
		t.Fatalf("direct tool should not activate tag prompt: %q", system)
	}
}

func TestToolDirectiveReportsExistingTools(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha"})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "先处理 @tool:alpha"); err != nil {
		t.Fatalf("HandleMessage first: %v", err)
	}
	if err := a.HandleMessage(context.Background(), "继续 @tool:alpha"); err != nil {
		t.Fatalf("HandleMessage second: %v", err)
	}
	if !strings.Contains(p.out.String(), "已存在工具：alpha") {
		t.Fatalf("missing existing notice: %q", p.out.String())
	}
}

func TestToolDirectiveInvalidStaysAsText(t *testing.T) {
	for _, directive := range []string{"@tool:nope", "@t:nope"} {
		t.Run(directive, func(t *testing.T) {
			p := &fakePlatform{}
			store := newTestStore(t)
			f := &fakeLLM{replies: []string{"done"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, store)
			registry := tool.NewRegistry()
			_ = registry.Register(tool.NewDiscoverTool(registry))
			a.SetToolRuntime(registry, nil)

			if err := a.HandleMessage(context.Background(), "hello "+directive); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			requests := f.chatRequests()
			if len(requests) != 1 {
				t.Fatalf("chat requests = %d", len(requests))
			}
			latest := requests[0].Messages[len(requests[0].Messages)-1]
			if got := llm.SegmentsContentText(latest.Segments); got != "hello "+directive {
				t.Fatalf("latest user content = %q", got)
			}
			if !strings.Contains(p.out.String(), "未找到或不可用的工具：nope") {
				t.Fatalf("missing invalid tool notice: %q", p.out.String())
			}
		})
	}
}

func TestToolDirectiveOnlyValidToolPreloadsNextTurn(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebExtractTool())
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "@tool:web_extract"); err != nil {
		t.Fatalf("HandleMessage preload: %v", err)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests after preload = %d", got)
	}
	if !strings.Contains(p.out.String(), "已注入工具：web_extract") {
		t.Fatalf("missing inject notice: %q", p.out.String())
	}
	if err := a.HandleMessage(context.Background(), "现在读取网页"); err != nil {
		t.Fatalf("HandleMessage chat: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool,web_extract" {
		t.Fatalf("preloaded tools missing in next turn: %s", toolNames(requests[0].Tools))
	}
}

func toolNames(schemas []llm.ToolSchema) string {

	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema.Function.Name)
	}
	return strings.Join(names, ",")
}

func TestSkillDirectiveInjectsDetailAndWrapper(t *testing.T) {
	for _, directive := range []string{"@skill:docx", "@skill：docx", "@s:docx", "@s：docx"} {
		t.Run(directive, func(t *testing.T) {
			p := &fakePlatform{}
			store := newTestStore(t)
			f := &fakeLLM{replies: []string{"done"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, store)
			a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
			registry := tool.NewRegistry()
			_ = registry.Register(tool.NewDiscoverTool(registry))
			_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX", activate: []string{"python_skill_run"}})
			_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
			a.SetToolRuntime(registry, nil)

			if err := a.HandleMessage(context.Background(), "处理这个 "+directive); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			requests := f.chatRequests()
			if len(requests) != 1 {
				t.Fatalf("chat requests = %d", len(requests))
			}
			if toolNames(requests[0].Tools) != "discover_tool,python_skill_run" {
				t.Fatalf("tools = %s", toolNames(requests[0].Tools))
			}
			latest := requests[0].Messages[len(requests[0].Messages)-1]
			content := llm.SegmentsContentText(latest.Segments)
			if content != "处理这个\n\n# DOCX" {
				t.Fatalf("latest user content = %q", content)
			}
			if strings.Contains(content, "系统预加载") || strings.Contains(content, "Skill: docx") {
				t.Fatalf("skill directive should not add synthetic headers: %q", content)
			}
		})
	}
}

func TestSkillDirectiveDeduplicatesElyphRuleCards(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "alpha", source: tool.SourceSkillAgent, detail: "#skill alpha - A", format: "elyph", ruleCard: "ELyph RULE"})
	_ = registry.Register(agentDetailTool{name: "beta", source: tool.SourceSkillAgent, detail: "#skill beta - B", format: "elyph", ruleCard: "ELyph RULE"})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "处理这个 @skill:alpha @skill:beta"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	content := llm.SegmentsContentText(requests[0].Messages[len(requests[0].Messages)-1].Segments)
	if strings.Count(content, "ELyph RULE") != 1 {
		t.Fatalf("rule card should appear once: %q", content)
	}
	if !strings.Contains(content, "#skill alpha - A") || !strings.Contains(content, "#skill beta - B") {
		t.Fatalf("missing skill content: %q", content)
	}
}

func TestSkillDirectiveDoesNotRepeatElyphRuleCardAcrossTurns(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done alpha", "done beta"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "alpha", source: tool.SourceSkillAgent, detail: "#skill alpha - A", format: "elyph", ruleCard: "ELyph RULE"})
	_ = registry.Register(agentDetailTool{name: "beta", source: tool.SourceSkillAgent, detail: "#skill beta - B", format: "elyph", ruleCard: "ELyph RULE"})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "处理这个 @skill:alpha"); err != nil {
		t.Fatalf("HandleMessage alpha: %v", err)
	}
	if err := a.HandleMessage(context.Background(), "继续 @skill:beta"); err != nil {
		t.Fatalf("HandleMessage beta: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	firstLatest := llm.SegmentsContentText(requests[0].Messages[len(requests[0].Messages)-1].Segments)
	if strings.Count(firstLatest, "ELyph RULE") != 1 || !strings.Contains(firstLatest, "#skill alpha - A") {
		t.Fatalf("first skill directive should include rule card and alpha detail: %q", firstLatest)
	}
	secondLatest := llm.SegmentsContentText(requests[1].Messages[len(requests[1].Messages)-1].Segments)
	if strings.Contains(secondLatest, "ELyph RULE") {
		t.Fatalf("second skill directive should not repeat rule card: %q", secondLatest)
	}
	if !strings.Contains(secondLatest, "#skill beta - B") {
		t.Fatalf("second skill directive should include beta detail: %q", secondLatest)
	}
	sessionRecord := onlySession(t, store, p)
	if !strings.Contains(sessionRecord.Metadata, `"shown_rule_card_formats":["elyph"]`) {
		t.Fatalf("metadata should persist shown rule card format: %q", sessionRecord.Metadata)
	}
}

func TestDiscoverToolDoesNotRepeatElyphRuleCardAcrossTurns(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "discover_tool", Args: `{"name":"alpha"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done alpha"}},
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_2", Name: "discover_tool", Args: `{"name":"beta"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done beta"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "alpha", source: tool.SourceSkillAgent, detail: "#skill alpha - A", format: "elyph", ruleCard: "ELyph RULE"})
	_ = registry.Register(agentDetailTool{name: "beta", source: tool.SourceSkillAgent, detail: "#skill beta - B", format: "elyph", ruleCard: "ELyph RULE"})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "发现 alpha"); err != nil {
		t.Fatalf("HandleMessage alpha: %v", err)
	}
	if err := a.HandleMessage(context.Background(), "发现 beta"); err != nil {
		t.Fatalf("HandleMessage beta: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 4 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	firstToolResult := llm.SegmentsContentText(requests[1].Messages[len(requests[1].Messages)-1].Segments)
	if strings.Count(firstToolResult, "ELyph RULE") != 1 || !strings.Contains(firstToolResult, "#skill alpha - A") {
		t.Fatalf("first discovery should include rule card and alpha detail: %q", firstToolResult)
	}
	secondToolResult := llm.SegmentsContentText(requests[3].Messages[len(requests[3].Messages)-1].Segments)
	if strings.Contains(secondToolResult, "ELyph RULE") {
		t.Fatalf("second discovery should not repeat rule card: %q", secondToolResult)
	}
	if !strings.Contains(secondToolResult, "#skill beta - B") {
		t.Fatalf("second discovery should include beta detail: %q", secondToolResult)
	}
	allSecondRequestText := chatRequestText(requests[3])
	if strings.Count(allSecondRequestText, "ELyph RULE") != 1 {
		t.Fatalf("previous rule card should remain in history exactly once: %q", allSecondRequestText)
	}
	sessionRecord := onlySession(t, store, p)
	if !strings.Contains(sessionRecord.Metadata, `"shown_rule_card_formats":["elyph"]`) {
		t.Fatalf("metadata should persist shown rule card format: %q", sessionRecord.Metadata)
	}
}

func TestSkillDirectiveInvalidStaysAsText(t *testing.T) {
	for _, directive := range []string{"@skill:nope", "@s:nope"} {
		t.Run(directive, func(t *testing.T) {
			p := &fakePlatform{}
			store := newTestStore(t)
			f := &fakeLLM{replies: []string{"done"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, store)
			registry := tool.NewRegistry()
			_ = registry.Register(tool.NewDiscoverTool(registry))
			a.SetToolRuntime(registry, nil)

			if err := a.HandleMessage(context.Background(), "hello "+directive); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			requests := f.chatRequests()
			if len(requests) != 1 {
				t.Fatalf("chat requests = %d", len(requests))
			}
			content := llm.SegmentsContentText(requests[0].Messages[len(requests[0].Messages)-1].Segments)
			if content != "hello "+directive {
				t.Fatalf("latest user content = %q", content)
			}
			if !strings.Contains(p.out.String(), "未找到或不可用的 Skill：nope") {
				t.Fatalf("missing invalid skill notice: %q", p.out.String())
			}
		})
	}
}

func TestSkillDirectiveOnlySkillSendsDetailAsUserMessage(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX", activate: []string{"python_skill_run"}})
	_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "@skill:docx"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool,python_skill_run" {
		t.Fatalf("tools = %s", toolNames(requests[0].Tools))
	}
	content := llm.SegmentsContentText(requests[0].Messages[len(requests[0].Messages)-1].Segments)
	if content != "# DOCX" {
		t.Fatalf("latest user content = %q", content)
	}
	if !strings.Contains(p.out.String(), "已注入 Skill：docx") {
		t.Fatalf("missing skill notice: %q", p.out.String())
	}
}

func TestSkillDiscoveryActivatesHiddenWrapperInSameTurn(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "discover_tool", Args: `{"name":"docx"}`}}, FinishReason: "tool_calls"}},
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_2", Name: "python_skill_run", Args: `{"skill":"docx","script":"scripts/a.py"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX", activate: []string{"python_skill_run"}})
	_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "处理 docx"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 3 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool" {
		t.Fatalf("initial tools = %s", toolNames(requests[0].Tools))
	}
	if toolNames(requests[1].Tools) != "discover_tool,python_skill_run" {
		t.Fatalf("wrapper not activated in same turn: %s", toolNames(requests[1].Tools))
	}
	if latestToolContent := llm.SegmentsContentText(requests[1].Messages[len(requests[1].Messages)-1].Segments); !strings.Contains(latestToolContent, "# DOCX") {
		t.Fatalf("skill detail should be returned as tool result text: %s", latestToolContent)
	}
	sessions, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil || len(sessions) == 0 {
		t.Fatalf("list sessions: %#v err=%v", sessions, err)
	}
	sessionRecord, err := store.Sessions().Get(context.Background(), sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sessionRecord.Metadata, "python_skill_run") {
		t.Fatalf("metadata should persist wrapper: %q", sessionRecord.Metadata)
	}
}

type agentDetailTool struct {
	name     string
	source   tool.Source
	detail   string
	format   string
	ruleCard string
	activate []string
}

func (t agentDetailTool) Name() string { return t.name }
func (t agentDetailTool) Info() tool.Info {
	return tool.Info{Name: t.name, Description: t.name, Source: t.source, Risk: tool.RiskLow}
}
func (t agentDetailTool) Schema() llm.ToolSchema { return llm.ToolSchema{} }
func (t agentDetailTool) Call(context.Context, tool.CallRequest) (*tool.Result, error) {
	return &tool.Result{Content: t.detail}, nil
}
func (t agentDetailTool) Detail() string {
	return tool.RenderDetailBlocks([]tool.DetailBlock{t.DetailBlock()})
}
func (t agentDetailTool) DetailBlock() tool.DetailBlock {
	return tool.DetailBlock{Content: t.detail, Format: t.format, RuleCard: t.ruleCard}
}
func (t agentDetailTool) ActivateTools() []string { return t.activate }

type agentWrapperTool struct {
	name   string
	hidden bool
	tags   []string
}

func (t agentWrapperTool) Name() string { return t.name }
func (t agentWrapperTool) Info() tool.Info {
	return tool.Info{Name: t.name, Description: t.name, Source: tool.SourceBuiltin, Risk: tool.RiskLow, Hidden: t.hidden, Tags: t.tags}
}
func (t agentWrapperTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: t.name, Parameters: map[string]any{"type": "object"}}}
}
func (t agentWrapperTool) Call(context.Context, tool.CallRequest) (*tool.Result, error) {
	return &tool.Result{Content: "ok"}, nil
}

func TestChatToolCallWithAssistantTextSkipsFallbackPreview(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{DeltaContent: "我先看一下。"}, {ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "看完了"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "看看目录"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if strings.Contains(p.preview.String(), "模型准备调用工具") {
		t.Fatalf("unexpected fallback preview: %q", p.preview.String())
	}
	if !strings.Contains(p.preview.String(), "正在调用 shell") || !strings.Contains(p.preview.String(), `"cmd":"ls"`) {
		t.Fatalf("missing cli tool arguments preview: %q", p.preview.String())
	}
	if !strings.Contains(p.out.String(), "我先看一下。") || !strings.Contains(p.out.String(), "看完了") {
		t.Fatalf("missing streamed text: %q", p.out.String())
	}
}

func TestNonCLIChatToolCallWithAssistantTextSkipsToolArgumentPreview(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{DeltaContent: "我先看一下。"}, {ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "看完了"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"qq": {"1"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ActorID: "qq:1", ScopeID: "private:1"})

	if err := a.HandleMessage(ctx, "看看目录"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if strings.Contains(p.preview.String(), "正在调用 shell") || strings.Contains(p.preview.String(), `"cmd":"ls"`) {
		t.Fatalf("non-cli should not receive assistant-text tool argument preview: %q", p.preview.String())
	}
	if !strings.Contains(p.out.String(), "我先看一下。") || !strings.Contains(p.out.String(), "看完了") {
		t.Fatalf("missing streamed text: %q", p.out.String())
	}
}

func TestToolCallAssistantEmoticonSendsBeforeFinalResponse(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{DeltaContent: "我先查一下[[微笑]]"}, {ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "查完了"}},
	}}
	store := newTestStore(t)
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	manager := hook.NewManager()
	configDir := t.TempDir()
	emoticonDir := filepath.Join(configDir, "emoticons", "微笑")
	if err := os.MkdirAll(emoticonDir, 0o755); err != nil {
		t.Fatalf("mkdir emoticon dir: %v", err)
	}
	imgPath := filepath.Join(emoticonDir, "01.png")
	if err := os.WriteFile(imgPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write emoticon image: %v", err)
	}
	// Shell script: read init frame, emit an emoticon output frame and cleaned text.
	scriptPath := filepath.Join(configDir, "emoticon_extract.sh")
	script := `#!/bin/sh
read init
img=$(ls emoticons/微笑/*.png 2>/dev/null | head -1)
printf '{"type":"output","outputs":[{"kind":"emoticon","name":"微笑","path":"%s"}]}\n' "$img"
printf '{"type":"done","message":{"text":"我先查一下"}}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	hooksTOML := fmt.Sprintf(`
[[rules]]
name = "emoticon_extract"
on = "llm.response.received"
priority = 1000
if = "llm.text"
op = "regex"
value = "\\[\\[[^\\[\\]]+\\]\\]"
actions = [
  { name = "extract", type = "exec", command = "sh ./emoticon_extract.sh", field = "llm.text", timing = "%s" },
]
`, delivery.DeliveryAfterAssistant)
	if err := os.WriteFile(filepath.Join(configDir, "hooks.toml"), []byte(hooksTOML), 0o644); err != nil {
		t.Fatalf("write hooks.toml: %v", err)
	}
	if err := hookbuiltin.RegisterAll(manager, hookbuiltin.Options{ConfigDir: configDir}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "看看目录"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	out := p.out.String()
	textIdx := strings.Index(out, "我先查一下")
	emoticonIdx := strings.Index(out, "[表情: 微笑]")
	finalIdx := strings.Index(out, "查完了")
	if textIdx < 0 || emoticonIdx < 0 || finalIdx < 0 {
		t.Fatalf("platform output = %q, want intermediate text, emoticon, and final text", out)
	}
	if !(textIdx < emoticonIdx && emoticonIdx < finalIdx) {
		t.Fatalf("invalid output order: %q", out)
	}
	if strings.Contains(out, "[[微笑]]") {
		t.Fatalf("platform output still contains raw token: %q", out)
	}
	session, err := a.sessions.Current(context.Background(), a.scope(context.Background()))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	messages, err := store.Messages().ListBySession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var foundRawIntermediate bool
	for _, msg := range messages {
		if msg.Role == storage.RoleAssistant && msg.Content == "我先查一下[[微笑]]" {
			foundRawIntermediate = true
		}
	}
	if !foundRawIntermediate {
		t.Fatalf("messages = %#v, want raw intermediate assistant text", messages)
	}
	if got := messages[len(messages)-1].Content; got != "查完了" {
		t.Fatalf("final assistant content = %q, want final response", got)
	}
}

func TestChatExecutesAllToolCallsInSameRound(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}, {ID: "call_2", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	a.SetToolConfig(config.ToolsConfig{MaxRoundsPerTurn: 1})
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "看看目录"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	followup := requests[1]
	seen := map[string]bool{}
	for _, msg := range followup.Messages {
		if msg.Role == llm.RoleTool && strings.Contains(llm.SegmentsContentText(msg.Segments), "stdout") {
			seen[msg.ToolCallID] = true
		}
		if strings.Contains(llm.SegmentsContentText(msg.Segments), "max_rounds_per_turn") {
			t.Fatalf("same-round tool call should not be skipped: %#v", msg)
		}
	}
	if !seen["call_1"] || !seen["call_2"] {
		t.Fatalf("not all same-round tool calls executed: %#v", followup.Messages)
	}
}

func TestRunBackgroundPreloadsShellWithContextActorAndAutoConfirmsSandboxShell(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"echo 'ok' > ./elnis_shell_tool_test.txt"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: `{"completed":true,"need_report":true,"report":"done"}`}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetToolConfig(config.ToolsConfig{MaxRoundsPerTurn: 2})
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)

	_, err := a.RunBackground(context.Background(), background.RunRequest{
		Kind:          background.KindElnis,
		Name:          "home/curl/event-1",
		Title:         "Elnis shell test",
		Platform:      "qqonebot",
		Actor:         security.Actor{ID: "elnis:home", Platform: "elnis", PlatformUserID: "home", Role: security.RoleSuperadmin},
		ScopeID:       "elnis:home/curl/event-1",
		Prompt:        "create a file",
		ToolListNames: []string{"discover_tool", "shell"},
		SandboxSubdir: "elnis/home",
	})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) < 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	var shellSchema llm.ToolSchema
	for _, schema := range requests[0].Tools {
		if schema.Function.Name == "discover_tool" {
			t.Fatalf("background request should not include discover_tool: %#v", requests[0].Tools)
		}
		if schema.Function.Name == "shell" {
			shellSchema = schema
		}
	}
	if shellSchema.Function.Name == "" {
		t.Fatalf("first request tools did not include preloaded shell: %#v", requests[0].Tools)
	}
	if !strings.Contains(shellSchema.Function.Description, "相对路径") {
		t.Fatalf("background shell schema description should mention relative paths: %q", shellSchema.Function.Description)
	}
	var shellResult string
	for _, msg := range requests[1].Messages {
		if msg.Role == llm.RoleTool && msg.Name == "shell" {
			shellResult = llm.SegmentsContentText(msg.Segments)
		}
	}
	if !strings.Contains(shellResult, "agent test shell stdout") {
		t.Fatalf("shell was not executed after background auto confirmation, result = %q", shellResult)
	}
}

func TestWorkSessionKeepsDiscoverTool(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"ok"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) == 0 {
		t.Fatal("no chat requests")
	}
	for _, schema := range requests[0].Tools {
		if schema.Function.Name == "discover_tool" {
			return
		}
	}
	t.Fatalf("ordinary work session should include discover_tool: %#v", requests[0].Tools)
}

func TestChatToolMaxRoundsPerTurnRequestsSummary(t *testing.T) {
	p := &fakePlatform{}
	secondStarted := make(chan struct{})
	secondRelease := make(chan struct{})
	f := &fakeLLM{
		chunks: [][]llm.StreamChunk{
			{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
			{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_2", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
			{{DeltaContent: "summary"}},
		},
		chatBlocks: []fakeLLMBlock{{}, {started: secondStarted, release: secondRelease}},
	}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	a.SetToolConfig(config.ToolsConfig{MaxRoundsPerTurn: 1})
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "看看目录") }()
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second LLM request did not start")
	}
	if err := a.HandleMessage(ctx, "这是追加"); err != nil {
		t.Fatalf("pending HandleMessage: %v", err)
	}
	close(secondRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleMessage: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool flow did not finish")
	}
	requests := f.chatRequests()
	if len(requests) != 3 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	summaryReq := requests[2]
	if len(summaryReq.Tools) != 0 {
		t.Fatalf("summary request should not include tools: %#v", summaryReq.Tools)
	}
	assistantIdx, skippedIdx, pendingIdx, summaryPromptIdx := -1, -1, -1, -1
	for i, msg := range summaryReq.Messages {
		text := llm.SegmentsContentText(msg.Segments)
		if len(msg.ToolCalls) > 0 && msg.ToolCalls[0].ID == "call_2" {
			assistantIdx = i
		}
		if msg.ToolCallID == "call_2" && strings.Contains(text, "max_rounds_per_turn") {
			skippedIdx = i
		}
		if msg.Role == llm.RoleUser && strings.Contains(text, "这是追加") {
			pendingIdx = i
		}
		if msg.Role == llm.RoleUser && strings.Contains(text, "总结当前进度") {
			summaryPromptIdx = i
		}
	}
	if assistantIdx < 0 || skippedIdx < 0 || pendingIdx < 0 || summaryPromptIdx < 0 {
		t.Fatalf("missing assistant/skipped/pending/summary message: %#v", summaryReq.Messages)
	}
	if !(assistantIdx < skippedIdx && skippedIdx < pendingIdx && pendingIdx < summaryPromptIdx) {
		t.Fatalf("invalid summary message order: assistant=%d skipped=%d pending=%d summary=%d messages=%#v", assistantIdx, skippedIdx, pendingIdx, summaryPromptIdx, summaryReq.Messages)
	}
	if !strings.Contains(p.out.String(), "summary") {
		t.Fatalf("missing summary output: %q", p.out.String())
	}
}

func TestLLMInterruptKeepsAppendConfirmationUntilConfirm(t *testing.T) {
	p := &fakePlatform{}
	block := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
	f := &fakeLLM{chatBlocks: []fakeLLMBlock{block}, replies: []string{"interrupted", "confirmed"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "1+1") }()
	select {
	case <-block.started:
	case <-time.After(time.Second):
		t.Fatal("first LLM request did not start")
	}
	waitRequestCount(t, f, 1)

	if err := a.HandleMessage(ctx, "stop"); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("interrupted turn: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("interrupted turn did not finish")
	}

	if err := a.HandleMessage(ctx, "同时计算2+2"); err != nil {
		t.Fatalf("append pending: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := f.requestCount(); got != 1 {
		t.Fatalf("request count after pending append = %d, want 1", got)
	}

	if err := a.HandleMessage(ctx, "再计算3+3"); err != nil {
		t.Fatalf("append pending 2: %v", err)
	}
	if err := a.HandleMessage(ctx, "y"); err != nil {
		t.Fatalf("confirm append: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d, want 2", len(requests))
	}
	got := llm.SegmentsContentText(requests[1].Messages[len(requests[1].Messages)-1].Segments)
	for _, want := range []string{"1+1", "stop", "同时计算2+2", "再计算3+3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("confirmed request content = %q, missing %q", got, want)
		}
	}
}

func TestAppendConfirmationBlocksNewSessionCommand(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"confirmed"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()
	session, err := a.sessions.Create(ctx, a.scope(ctx), "current")
	if err != nil {
		t.Fatal(err)
	}
	if !a.turns.StartLLM(session.ID, "1+1") {
		t.Fatal("StartLLM returned false")
	}
	if !a.turns.InterruptLLM(session.ID, "stop") {
		t.Fatal("InterruptLLM returned false")
	}

	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatalf("/new: %v", err)
	}
	current, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != session.ID {
		t.Fatalf("current session = %s, want %s", current.ID, session.ID)
	}
	if snapshot := a.turns.Snapshot(session.ID); snapshot.Phase != turn.PhaseAwaitAppendConfirm || snapshot.PendingCount != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if got := p.out.String(); !strings.Contains(got, appendConfirmPrompt) {
		t.Fatalf("output = %q, want append confirmation prompt", got)
	}
}

func TestToolPhasePendingInputInjectedBeforeFollowupLLM(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "slow", Args: `{}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "final"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	started := make(chan struct{})
	release := make(chan struct{})
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(slowTool{started: started, release: release})
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "先查") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	current, err := a.sessions.Current(ctx, a.scope(context.Background()))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	active := a.requests.ListBySession(current.ID)
	if len(active) == 0 {
		t.Fatal("expected active tool request")
	}
	if err := a.HandleMessage(ctx, "这是追问"); err != nil {
		t.Fatalf("pending HandleMessage: %v", err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tool flow error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool flow did not finish")
	}

	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	var foundPending bool
	for _, msg := range requests[1].Messages {
		if msg.Role == llm.RoleUser && strings.Contains(llm.SegmentsContentText(msg.Segments), "这是追问") {
			foundPending = true
		}
	}
	if !foundPending {
		t.Fatalf("pending input was not injected: %#v", requests[1].Messages)
	}
	messages, err := store.Messages().ListBySession(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	var persistedPending bool
	for _, msg := range messages {
		if msg.Role == storage.RoleUser && strings.Contains(msg.Content, "这是追问") {
			persistedPending = true
		}
	}
	if !persistedPending {
		t.Fatalf("pending input was not persisted: %#v", messages)
	}
	if !strings.Contains(p.out.String(), "已追加") {
		t.Fatalf("missing pending acknowledgement: %q", p.out.String())
	}
}

func TestToolChildRequestCancelReturnsToolMessageAndContinuesTurn(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "slow", Args: `{}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "final after cancel"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	started := make(chan struct{})
	release := make(chan struct{})
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(slowTool{started: started, release: release})
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "先查") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	if err := a.HandleMessage(ctx, "/stop 1.1"); err != nil {
		t.Fatalf("stop tool: %v", err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tool flow error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool flow did not finish")
	}

	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	var canceledTool bool
	for _, msg := range requests[1].Messages {
		if msg.Role == llm.RoleTool && msg.ToolCallID == "call_1" && strings.Contains(llm.SegmentsContentText(msg.Segments), "canceled by user") {
			canceledTool = true
		}
	}
	if !canceledTool {
		t.Fatalf("followup request missing canceled tool message: %#v", requests[1].Messages)
	}
	if !strings.Contains(p.out.String(), "final after cancel") {
		t.Fatalf("missing final output: %q", p.out.String())
	}
}

func TestTurnRequestCancelStopsWithoutPersistingToolTranscript(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "slow", Args: `{}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "should not run"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	started := make(chan struct{})
	release := make(chan struct{})
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(slowTool{started: started, release: release})
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "先查") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	current, err := a.sessions.Current(ctx, a.scope(context.Background()))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if err := a.HandleMessage(ctx, "/stop 1"); err != nil {
		t.Fatalf("stop turn: %v", err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tool flow error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool flow did not finish")
	}

	if got := len(f.chatRequests()); got != 1 {
		t.Fatalf("chat requests = %d, want 1", got)
	}
	messages, err := store.Messages().ListBySession(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range messages {
		if msg.Role == storage.RoleAssistant && strings.Contains(msg.Metadata, "call_1") {
			t.Fatalf("assistant tool call transcript persisted after turn stop: %#v", messages)
		}
		if msg.Role == storage.RoleTool && msg.ToolCallID == "call_1" {
			t.Fatalf("tool transcript persisted after turn stop: %#v", messages)
		}
	}
}

func TestToolPhasePendingInputDuringFollowupLLMContinuesSameTurn(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	followupStarted := make(chan struct{})
	followupRelease := make(chan struct{})
	f := &fakeLLM{
		chunks: [][]llm.StreamChunk{
			{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
			{{DeltaContent: "almost final"}},
			{{DeltaContent: "final with pending"}},
		},
		chatBlocks: []fakeLLMBlock{{}, {started: followupStarted, release: followupRelease}},
	}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "看看目录") }()
	select {
	case <-followupStarted:
	case <-time.After(time.Second):
		t.Fatal("followup LLM did not start")
	}
	current, err := a.sessions.Current(ctx, a.scope(context.Background()))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if err := a.HandleMessage(ctx, "收尾前补一句"); err != nil {
		t.Fatalf("pending HandleMessage: %v", err)
	}
	close(followupRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tool flow error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool flow did not finish")
	}

	requests := f.chatRequests()
	if len(requests) != 3 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	retry := requests[2]
	var sawAssistant, sawPending bool
	for _, msg := range retry.Messages {
		if msg.Role == llm.RoleAssistant && llm.SegmentsContentText(msg.Segments) == "almost final" {
			sawAssistant = true
		}
		if msg.Role == llm.RoleUser && strings.Contains(llm.SegmentsContentText(msg.Segments), "收尾前补一句") {
			sawPending = true
		}
	}
	if !sawAssistant || !sawPending {
		t.Fatalf("retry request missing assistant context or pending input: %#v", retry.Messages)
	}
	messages, err := store.Messages().ListBySession(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasStoredUserMessage(messages, "收尾前补一句") {
		t.Fatalf("pending input was not persisted: %#v", messages)
	}
}

func TestToolPhasePendingInputIncludedInMaxRoundsSummary(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	secondStarted := make(chan struct{})
	secondRelease := make(chan struct{})
	f := &fakeLLM{
		chunks: [][]llm.StreamChunk{
			{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
			{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_2", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
			{{DeltaContent: "summary with pending"}},
		},
		chatBlocks: []fakeLLMBlock{{}, {started: secondStarted, release: secondRelease}},
	}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	a.SetToolConfig(config.ToolsConfig{MaxRoundsPerTurn: 1})
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "看看目录") }()
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second LLM did not start")
	}
	current, err := a.sessions.Current(ctx, a.scope(context.Background()))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if err := a.HandleMessage(ctx, "总结前补一句"); err != nil {
		t.Fatalf("pending HandleMessage: %v", err)
	}
	close(secondRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tool flow error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool flow did not finish")
	}

	requests := f.chatRequests()
	if len(requests) != 3 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	summaryReq := requests[2]
	var sawPending, sawSummaryPrompt bool
	for _, msg := range summaryReq.Messages {
		if msg.Role == llm.RoleUser && strings.Contains(llm.SegmentsContentText(msg.Segments), "总结前补一句") {
			sawPending = true
		}
		if msg.Role == llm.RoleUser && strings.Contains(llm.SegmentsContentText(msg.Segments), "总结当前进度") {
			sawSummaryPrompt = true
		}
	}
	if !sawPending || !sawSummaryPrompt {
		t.Fatalf("summary request missing pending input or summary prompt: %#v", summaryReq.Messages)
	}
	messages, err := store.Messages().ListBySession(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasStoredUserMessage(messages, "总结前补一句") {
		t.Fatalf("pending input was not persisted: %#v", messages)
	}
}

func hasStoredUserMessage(messages []storage.Message, content string) bool {
	for _, msg := range messages {
		if msg.Role == storage.RoleUser && strings.Contains(msg.Content, content) {
			return true
		}
	}
	return false
}

type agentShellTool struct{}

func (agentShellTool) Name() string { return "shell" }
func (agentShellTool) Info() tool.Info {
	return tool.Info{Name: "shell", Description: "fast test shell", Source: tool.SourceBuiltin, Risk: tool.RiskHigh}
}
func (agentShellTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Function: llm.ToolFunctionSchema{Name: "shell", Parameters: map[string]any{"type": "object"}}}
}
func (agentShellTool) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	return builtin.NewShellTool().AssessRisk(ctx, req)
}
func (agentShellTool) Call(context.Context, tool.CallRequest) (*tool.Result, error) {
	return &tool.Result{Content: "agent test shell stdout"}, nil
}

func newAgentShellTool() tool.Tool { return agentShellTool{} }

type slowTool struct {
	started chan struct{}
	release chan struct{}
}

func (t slowTool) Name() string { return "slow" }
func (t slowTool) Info() tool.Info {
	return tool.Info{Name: "slow", Description: "slow test tool", Source: tool.SourceBuiltin, Risk: tool.RiskLow}
}
func (t slowTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Function: llm.ToolFunctionSchema{Name: "slow", Parameters: map[string]any{"type": "object"}}}
}
func (t slowTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	close(t.started)
	select {
	case <-t.release:
		return &tool.Result{Content: "slow result"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestStatusRestoresUsageFromSessionMetadata(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: "reply", Usage: &llm.Usage{TotalTokens: 123, CacheHitTokens: 7}}}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)

	if err := a.HandleMessage(context.Background(), "hello usage"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	sessions, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil || len(sessions) == 0 {
		t.Fatalf("list sessions: %#v err=%v", sessions, err)
	}
	resumed := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
	if err := resumed.HandleMessage(context.Background(), "/resume "+sessions[0].ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	p.out.Reset()
	if err := resumed.HandleMessage(context.Background(), "/status"); err != nil {
		t.Fatalf("status: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "tokens：123（命中：7）") {
		t.Fatalf("usage not restored in status: %q", got)
	}
}

func TestRiskConfirmationStopUsesStopCommandWithoutToolError(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"rm out.txt"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "should not continue"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "危险命令") }()

	var current *storage.Session
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session, err := a.sessions.Current(ctx, a.scope(context.Background()))
		if err == nil && a.turns.Snapshot(session.ID).Phase == turn.PhaseAwaitRiskConfirm {
			current = session
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if current == nil {
		t.Fatal("did not enter risk confirmation phase")
	}

	if err := a.HandleMessage(ctx, "/stop"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("chat returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("chat did not stop")
	}
	if got := a.turns.Snapshot(current.ID).Phase; got != turn.PhaseIdle {
		t.Fatalf("turn phase = %s", got)
	}
	if requests := f.chatRequests(); len(requests) != 1 {
		t.Fatalf("chat requests = %d, want 1", len(requests))
	}
	out := p.out.String() + p.preview.String()
	if strings.Contains(out, "should not continue") || strings.Contains(out, "stopped while waiting") || strings.Contains(out, "assistant: error") {
		t.Fatalf("unexpected stop output: %q", out)
	}
	if !strings.Contains(p.out.String(), "stopped") {
		t.Fatalf("missing stop command output: %q", p.out.String())
	}
}

func TestRiskConfirmationDetailShowsFullArgumentsWithoutResolving(t *testing.T) {
	p := &fakePlatform{}
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()
	session, err := a.sessions.Create(ctx, a.scope(context.Background()), "confirm detail")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
		t.Fatal("failed to enter tool phase")
	}
	fullArgs := `{"cmd":"echo 12345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890 > out.txt"}`
	done := make(chan turn.RiskConfirmationResponse, 1)
	go func() {
		resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_1", ToolName: "shell", Arguments: fullArgs, Risk: "high"})
		done <- resp
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		time.Sleep(10 * time.Millisecond)
	}
	if a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		t.Fatal("did not enter risk confirmation phase")
	}
	if err := a.HandleMessage(ctx, "/detail"); err != nil {
		t.Fatalf("detail: %v", err)
	}
	detailOutput := p.out.String()
	if strings.Contains(detailOutput, fullArgs) {
		t.Fatalf("detail should format args instead of raw JSON: %q", detailOutput)
	}
	for _, want := range []string{"cmd: ", "echo 1234567890", " > out.txt"} {
		if !strings.Contains(detailOutput, want) {
			t.Fatalf("detail missing %q: %q", want, detailOutput)
		}
	}
	select {
	case resp := <-done:
		t.Fatalf("detail should not resolve confirmation: %#v", resp)
	default:
	}
	if got := a.turns.Snapshot(session.ID).Phase; got != turn.PhaseAwaitRiskConfirm {
		t.Fatalf("phase = %s, want await risk confirm", got)
	}
	if err := a.HandleMessage(ctx, "/confirm"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	select {
	case resp := <-done:
		if !resp.Confirmed {
			t.Fatalf("response = %#v", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("confirm did not resolve")
	}
}

func TestRiskConfirmationDetailUsesToolProvidedDetail(t *testing.T) {
	p := &fakePlatform{}
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()
	session, err := a.sessions.Create(ctx, a.scope(context.Background()), "confirm custom detail")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
		t.Fatal("failed to enter tool phase")
	}
	done := make(chan turn.RiskConfirmationResponse, 1)
	go func() {
		resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_1", ToolName: "edit_file", Arguments: `{"path":"a.txt"}`, Risk: "high", Detail: "文件：a.txt\n编辑 1/1：替换行\n新内容：\n  line 1\n  line 2"})
		done <- resp
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		time.Sleep(10 * time.Millisecond)
	}
	if a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		t.Fatal("did not enter risk confirmation phase")
	}
	if err := a.HandleMessage(ctx, "/detail"); err != nil {
		t.Fatalf("detail: %v", err)
	}
	out := p.out.String()
	for _, want := range []string{"文件：a.txt", "编辑 1/1：替换行", "  line 1\n", "  line 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "path: a.txt") {
		t.Fatalf("detail should prefer tool detail over fallback: %q", out)
	}
	select {
	case resp := <-done:
		t.Fatalf("detail should not resolve confirmation: %#v", resp)
	default:
	}
}

func TestRiskConfirmationDetailFormatsEscapedNewlines(t *testing.T) {
	p := &fakePlatform{}
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()
	session, err := a.sessions.Create(ctx, a.scope(context.Background()), "confirm detail newlines")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
		t.Fatal("failed to enter tool phase")
	}
	done := make(chan turn.RiskConfirmationResponse, 1)
	go func() {
		resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_1", ToolName: "edit_file", Arguments: `{"path":"a.txt","content":"line 1\nline 2\nline 3"}`, Risk: "high"})
		done <- resp
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		time.Sleep(10 * time.Millisecond)
	}
	if a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		t.Fatal("did not enter risk confirmation phase")
	}
	if err := a.HandleMessage(ctx, "/detail"); err != nil {
		t.Fatalf("detail: %v", err)
	}
	out := p.out.String()
	if strings.Contains(out, `line 1\nline 2`) {
		t.Fatalf("detail still contains escaped newlines: %q", out)
	}
	for _, want := range []string{"content: |\n", "  line 1\n", "  line 2\n", "  line 3\n", "path: a.txt"} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail missing %q: %q", want, out)
		}
	}
	select {
	case resp := <-done:
		t.Fatalf("detail should not resolve confirmation: %#v", resp)
	default:
	}
}

func TestRiskConfirmationConfirmToolAndConfirmAllAliases(t *testing.T) {
	t.Run("confirm tool alias", func(t *testing.T) {
		p := &fakePlatform{}
		a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
		ctx := context.Background()
		session, err := a.sessions.Create(ctx, a.scope(context.Background()), "confirm tool")
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
			t.Fatal("failed to enter tool phase")
		}
		done := make(chan turn.RiskConfirmationResponse, 1)
		go func() {
			resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_1", ToolName: "shell", Arguments: `{"cmd":"rm x"}`, Risk: "high"})
			done <- resp
		}()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
			time.Sleep(10 * time.Millisecond)
		}
		if err := a.HandleMessage(ctx, "/ct"); err != nil {
			t.Fatalf("confirm tool alias: %v", err)
		}
		select {
		case resp := <-done:
			if !resp.Confirmed || !resp.ConfirmTool {
				t.Fatalf("response = %#v", resp)
			}
		case <-time.After(time.Second):
			t.Fatal("confirm tool did not resolve")
		}
	})

	t.Run("confirm all alias", func(t *testing.T) {
		p := &fakePlatform{}
		a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
		ctx := context.Background()
		session, err := a.sessions.Create(ctx, a.scope(context.Background()), "confirm all")
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
			t.Fatal("failed to enter tool phase")
		}
		done := make(chan turn.RiskConfirmationResponse, 1)
		go func() {
			resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_2", ToolName: "cron", Arguments: `{}`, Risk: "high"})
			done <- resp
		}()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
			time.Sleep(10 * time.Millisecond)
		}
		if err := a.HandleMessage(ctx, "/ca"); err != nil {
			t.Fatalf("confirm all alias: %v", err)
		}
		select {
		case resp := <-done:
			if !resp.Confirmed || !resp.ConfirmAll {
				t.Fatalf("response = %#v", resp)
			}
		case <-time.After(time.Second):
			t.Fatal("confirm all did not resolve")
		}
	})
}

func TestRiskConfirmationCompletionAndConfirmAlias(t *testing.T) {

	p := &fakePlatform{}
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()
	session, err := a.sessions.Create(ctx, a.scope(context.Background()), "confirm completion")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
		t.Fatal("failed to enter tool phase")
	}
	done := make(chan turn.RiskConfirmationResponse, 1)
	go func() {
		resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_1", ToolName: "shell", Arguments: `{\"cmd\":\"rm x\"}`, Risk: "high"})
		done <- resp
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		time.Sleep(10 * time.Millisecond)
	}
	if a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		t.Fatal("did not enter risk confirmation phase")
	}
	if !containsAll(a.Complete("/c"), []string{"/confirm", "/c", "/confirmtool", "/ct", "/confirmall", "/ca"}) {
		t.Fatalf("confirm completion /c = %#v", a.Complete("/c"))
	}
	for _, command := range []string{"detail", "details", "confirm", "c", "confirmtool", "ct", "confirmall", "ca", "reject", "stop"} {

		if got := a.Complete("/" + command); len(got) == 0 {
			t.Fatalf("missing completion for %s", command)
		}
	}
	if err := a.HandleMessage(ctx, "/c"); err != nil {
		t.Fatalf("confirm alias: %v", err)
	}
	select {
	case resp := <-done:
		if !resp.Confirmed {
			t.Fatalf("response = %#v", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("confirmation alias did not resolve")
	}
}

func TestSoulPromptAndToolsByMode(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"work reply", "chat reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.soul = staticSoulProvider{Prompt: "SOUL ONLY"}
	a.rebuildSystemPrompt()
	tools := &recordingToolProvider{tools: []llm.ToolSchema{{Function: llm.ToolFunctionSchema{Name: "discover_tool", Description: "discover tools", Parameters: map[string]any{"type": "object"}}}}}
	a.SetToolProvider(tools)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "hello work"); err != nil {
		t.Fatalf("work HandleMessage: %v", err)
	}
	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.HandleMessage(ctx, "/chat"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if err := a.HandleMessage(ctx, "hello chat"); err != nil {
		t.Fatalf("chat HandleMessage: %v", err)
	}

	chatRequests := f.chatRequests()
	if len(chatRequests) != 2 {
		t.Fatalf("chat request count = %d", len(chatRequests))
	}
	for _, req := range chatRequests {
		systemPrompt := llm.SegmentsContentText(req.Messages[0].Segments)
		if systemPrompt != "SOUL ONLY" {
			t.Fatalf("system prompt = %q", systemPrompt)
		}
		if strings.Contains(systemPrompt, "discover_tool") || strings.Contains(systemPrompt, "work") {
			t.Fatalf("system prompt polluted: %q", systemPrompt)
		}
	}
	if len(chatRequests[0].Tools) != 1 || chatRequests[0].Tools[0].Function.Name != "discover_tool" {
		t.Fatalf("work tools = %#v", chatRequests[0].Tools)
	}
	if len(chatRequests[1].Tools) != 0 {
		t.Fatalf("chat tools = %#v", chatRequests[1].Tools)
	}
	if tools.calls != 1 {
		t.Fatalf("tool provider calls = %d", tools.calls)
	}
}

func TestRunBackgroundPreloadsSkillDetailAndActivatedHiddenWrapper(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":true,"report":"ok"}`}}}}
	platform := &fakePlatform{}
	a := New(platform, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX\n\nUse scripts/convert.py", activate: []string{"python_skill_run"}})
	_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
	a.SetToolRuntime(registry, nil)

	_, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "skill-test", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"docx"}})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if got := toolNames(requests[0].Tools); got != "python_skill_run" {
		t.Fatalf("tools = %s", got)
	}
	systemPrompt := llm.SegmentsContentText(requests[0].Messages[0].Segments)
	if strings.Contains(systemPrompt, "当前可用工具名称") || strings.Contains(systemPrompt, "discover_tool") {
		t.Fatalf("background system prompt should not list tool names: %q", systemPrompt)
	}
	latest := requests[0].Messages[len(requests[0].Messages)-1]
	content := llm.SegmentsContentText(latest.Segments)
	if !strings.Contains(content, "[系统预加载 Skill]") || !strings.Contains(content, "# DOCX") || !strings.Contains(content, "[后台任务]") || !strings.Contains(content, "run") {
		t.Fatalf("skill prompt missing content: %q", content)
	}
	if strings.Contains(content, "shell") || strings.Contains(content, "discover_tool") {
		t.Fatalf("skill prompt should not mention unavailable tools: %q", content)
	}
}

func TestRunBackgroundUsesWorkModeWhenDefaultModeIsChat(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":false,"report":"ok"}`}}}}
	platform := &fakePlatform{}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "default", Model: "test-model"},
		storage.SessionModeChat: {Provider: "default", Model: "test-model"},
	}
	a := NewWithOptions(platform, f, "default", modeModels, map[string]config.ProviderConfig{"default": {}}, "", store, []string{"/"}, session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeChat}, config.ModelSelection{}, nil, "", nil, "")
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebExtractTool())
	a.SetToolRuntime(registry, nil)

	result, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "chat-default", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"web_extract"}})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	sessionRecord, err := store.Sessions().Get(ctx, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sessionRecord.Mode != storage.SessionModeWork {
		t.Fatalf("background mode = %q", sessionRecord.Mode)
	}
	if !strings.Contains(sessionRecord.Metadata, `"background_kind":"cron"`) {
		t.Fatalf("background metadata = %q", sessionRecord.Metadata)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if got := toolNames(requests[0].Tools); got != "web_extract" {
		t.Fatalf("tools = %s", got)
	}
	systemPrompt := llm.SegmentsContentText(requests[0].Messages[0].Segments)
	if strings.Contains(systemPrompt, "当前可用工具名称") || strings.Contains(systemPrompt, "discover_tool") {
		t.Fatalf("background system prompt should not list tool names: %q", systemPrompt)
	}
}

func TestRunBackgroundRepairsReusedSessionModeAndMetadata(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	oldSession := &storage.Session{OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "cron:old", Mode: storage.SessionModeChat, Title: "old", Status: storage.SessionStatusActive, Metadata: `{"title_renamed":true}`}
	if err := store.Sessions().Create(ctx, oldSession); err != nil {
		t.Fatal(err)
	}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":false,"report":"ok"}`}}}}
	platform := &fakePlatform{}
	a := New(platform, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebExtractTool())
	a.SetToolRuntime(registry, nil)

	_, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "old", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, ScopeID: "cron:old", SessionID: oldSession.ID, Prompt: "run", ToolListNames: []string{"web_extract"}, Metadata: map[string]string{"cron_job_name": "old"}})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	repaired, err := store.Sessions().Get(ctx, oldSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Mode != storage.SessionModeWork {
		t.Fatalf("repaired mode = %q", repaired.Mode)
	}
	if !strings.Contains(repaired.Metadata, `"background_kind":"cron"`) || !strings.Contains(repaired.Metadata, `"cron_job_name":"old"`) {
		t.Fatalf("repaired metadata = %q", repaired.Metadata)
	}
	requests := f.chatRequests()
	if len(requests) != 1 || toolNames(requests[0].Tools) != "web_extract" {
		t.Fatalf("requests=%d tools=%q", len(requests), toolNames(requests[0].Tools))
	}
}

func TestRunBackgroundPreloadsMixedToolAndSkillWithoutSkillSchema(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":true,"report":"ok"}`}}}}
	platform := &fakePlatform{}
	a := New(platform, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebExtractTool())
	_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX", activate: []string{"python_skill_run"}})
	_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
	a.SetToolRuntime(registry, nil)

	_, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "mixed-test", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"web_extract", "docx"}})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	gotTools := toolNames(requests[0].Tools)
	if !strings.Contains(gotTools, "web_extract") || !strings.Contains(gotTools, "python_skill_run") {
		t.Fatalf("tools = %s", gotTools)
	}
	if strings.Contains(gotTools, "docx") {
		t.Fatalf("skill itself should not be top-level schema: %s", gotTools)
	}
}

func TestRunCronMessagePreloadsToolListNamesWithoutDiscoverTool(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":true,"report":"ok"}`}}}}
	platform := &fakePlatform{}
	a := New(platform, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebSearchTool())
	_ = registry.Register(builtin.NewWebExtractTool())
	a.SetToolRuntime(registry, nil)

	_, err := a.RunCronMessage(ctx, elcron.RunCronMessageRequest{JobName: "test", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"web_search", "web"}})
	if err != nil {
		t.Fatalf("RunCronMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "web_extract,web_search" {
		t.Fatalf("tools = %s", toolNames(requests[0].Tools))
	}
	if got := platform.out.String(); got != "" {
		t.Fatalf("background cron wrote platform output: %q", got)
	}
	if got := platform.reasoning.String(); got != "" {
		t.Fatalf("background cron wrote reasoning output: %q", got)
	}
	if platform.statusCount != 0 {
		t.Fatalf("background cron published runtime status: count=%d last=%#v", platform.statusCount, platform.lastStatus)
	}
}

func TestRunCronMessageToolPhaseDoesNotPublishRuntimeStatus(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	platform := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call-1", Name: "discover_tool", Args: `{"name":"web_search"}`}}}},
		{{DeltaContent: `{"completed":true,"need_report":false,"report":"ok"}`}},
	}}
	a := New(platform, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebSearchTool())
	a.SetToolRuntime(registry, nil)

	_, err := a.RunCronMessage(ctx, elcron.RunCronMessageRequest{JobName: "tool-status", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run"})
	if err != nil {
		t.Fatalf("RunCronMessage: %v", err)
	}
	if got := platform.out.String(); got != "" {
		t.Fatalf("background cron tool phase wrote platform output: %q", got)
	}
	if platform.statusCount != 0 {
		t.Fatalf("background cron tool phase published runtime status: count=%d last=%#v", platform.statusCount, platform.lastStatus)
	}
}

func TestRunCronMessageReturnsRawAssistantTextForJSONParsing(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	a := New(&fakePlatform{}, &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":true,"report":"ok"}`}}}}, "test-model", config.ProviderConfig{}, store)
	cronSession := &storage.Session{OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "cron:test", Mode: storage.SessionModeWork, Title: "Cron", Status: storage.SessionStatusActive}
	if err := store.Sessions().Create(ctx, cronSession); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: cronSession.ID, Role: storage.RoleAssistant, Content: "可见文本", Metadata: assistantRawTextMetadata("可见文本", `{"completed":true,"need_report":true,"report":"ok"}`)}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	message, err := a.latestAssistantMessage(ctx, cronSession.ID)
	if err != nil {
		t.Fatalf("latestAssistantMessage: %v", err)
	}
	text := message.Content
	if rawText := assistantRawTextFromMetadata(message.Metadata); rawText != "" {
		text = rawText
	}
	if text != `{"completed":true,"need_report":true,"report":"ok"}` {
		t.Fatalf("text = %q", text)
	}
}

func TestChatCommandSuggestsNewForWorkHistory(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "work history"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, "/chat"); err != nil {
		t.Fatalf("chat command: %v", err)
	}
	if !strings.Contains(p.out.String(), "run /new then /chat") {
		t.Fatalf("unexpected chat output: %q", p.out.String())
	}
	current, err := a.sessions.Current(ctx, a.scope(context.Background()))
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current.Mode != storage.SessionModeWork {
		t.Fatalf("mode = %q", current.Mode)
	}
}

func TestDefaultModeFromStateAppliesToNewSessions(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"chat reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.sessions = session.NewServiceWithConfig(store, session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeChat}, a.titleGen, nil)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "hello default chat"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	current, err := a.sessions.Current(ctx, a.scope(context.Background()))
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current.Mode != storage.SessionModeChat {
		t.Fatalf("mode = %q", current.Mode)
	}
}

func TestNewSessionsResumeCommands(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"first", "second"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("chat hello: %v", err)
	}
	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.HandleMessage(ctx, "after new"); err != nil {
		t.Fatalf("chat after new: %v", err)
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, "/sessions"); err != nil {
		t.Fatalf("sessions: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "sessions:") || !strings.Contains(got, "[current]") {
		t.Fatalf("missing current sessions output: %q", got)
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, "/resume 2"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got = p.out.String()
	if !strings.Contains(got, "resumed session:") || !strings.Contains(got, "recent messages:") {
		t.Fatalf("missing resume history output: %q", got)
	}
	if !strings.Contains(got, "user: hello") || !strings.Contains(got, "assistant: first") {
		t.Fatalf("missing resumed messages: %q", got)
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, "/resume"); err != nil {
		t.Fatalf("resume list: %v", err)
	}
	if !strings.Contains(p.out.String(), "[current]") {
		t.Fatalf("missing current marker in resume list: %q", p.out.String())
	}
	if err := a.HandleMessage(ctx, "/status"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(p.out.String(), "session status:") {
		t.Fatalf("missing status output: %q", p.out.String())
	}
}

func TestLLMResponseHookRewritesOutputButPersistsRawAssistantContent(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"raw response"}}
	store := newTestStore(t)
	a := New(p, f, "m", config.ProviderConfig{}, store)
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMResponseReceived, Priority: 0, Name: "test.clean-response", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.LLM.Text = "cleaned response"
		return event, nil
	})}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	session, err := a.sessions.Current(context.Background(), a.scope(context.Background()))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	messages, err := store.Messages().ListBySession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	got := messages[len(messages)-1].Content
	if got != "raw response" {
		t.Fatalf("assistant content = %q, want raw response", got)
	}
	if !strings.Contains(p.out.String(), "cleaned response") {
		t.Fatalf("platform output = %q, want cleaned response", p.out.String())
	}
}

func TestAgentOutputHookCanRewritePlatformOutput(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"raw"}}
	a := New(p, f, "m", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointAgentOutputPrepared, Priority: 0, Name: "test.output", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		if llm.SegmentsTextOnly(event.Message.Segments) == "raw" {
			event.Message.Segments = llm.ReplaceSegmentText(event.Message.Segments, regexp.MustCompile(regexp.QuoteMeta("raw")), "visible", false)
		}
		return event, nil
	})}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !strings.Contains(p.out.String(), "visible") {
		t.Fatalf("platform output = %q, want rewritten output", p.out.String())
	}
}

func TestEmoticonHookSendsSeparateOutputAndCleansPersistedContent(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"[[微笑]] 像这样~"}}
	store := newTestStore(t)
	a := New(p, f, "m", config.ProviderConfig{}, store)
	manager := hook.NewManager()
	configDir := t.TempDir()
	emoticonDir := filepath.Join(configDir, "emoticons", "微笑")
	if err := os.MkdirAll(emoticonDir, 0o755); err != nil {
		t.Fatalf("mkdir emoticon dir: %v", err)
	}
	imgPath := filepath.Join(emoticonDir, "01.png")
	if err := os.WriteFile(imgPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write emoticon image: %v", err)
	}
	scriptPath := filepath.Join(configDir, "emoticon_extract.sh")
	script := `#!/bin/sh
read init
img=$(ls emoticons/微笑/*.png 2>/dev/null | head -1)
printf '{"type":"output","outputs":[{"kind":"emoticon","name":"微笑","path":"%s"}]}\n' "$img"
printf '{"type":"done","message":{"text":"像这样~"}}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	hooksTOML := `
[[rules]]
name = "emoticon_extract"
on = "llm.response.received"
priority = 1000
if = "llm.text"
op = "regex"
value = "\\[\\[[^\\[\\]]+\\]\\]"
actions = [
  { name = "extract", type = "exec", command = "sh ./emoticon_extract.sh", field = "llm.text" },
]
`
	if err := os.WriteFile(filepath.Join(configDir, "hooks.toml"), []byte(hooksTOML), 0o644); err != nil {
		t.Fatalf("write hooks.toml: %v", err)
	}
	if err := hookbuiltin.RegisterAll(manager, hookbuiltin.Options{ConfigDir: configDir}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "说个微笑"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	out := p.out.String()
	emoticonIdx := strings.Index(out, "[表情: 微笑]")
	textIdx := strings.Index(out, "像这样~")
	if emoticonIdx < 0 || textIdx < 0 || emoticonIdx > textIdx {
		t.Fatalf("platform output = %q, want separate emoticon fallback and cleaned text", out)
	}
	if strings.Contains(out, "[[微笑]]") {
		t.Fatalf("platform output still contains raw token: %q", out)
	}
	session, err := a.sessions.Current(context.Background(), a.scope(context.Background()))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	messages, err := store.Messages().ListBySession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	got := messages[len(messages)-1].Content
	if got != "[[微笑]] 像这样~" {
		t.Fatalf("assistant content = %q, want raw text", got)
	}
}

func TestRegularUserCanUseOwnDataSlashCommands(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"ok"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "cli", PlatformUserID: "regular", ScopeID: "regular"})

	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatalf("/new: %v", err)
	}
	if !strings.Contains(p.out.String(), "created new session") {
		t.Fatalf("/new output = %q", p.out.String())
	}
	p.out.Reset()

	if _, err := a.sessions.Create(ctx, a.scope(ctx), "mine"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := a.HandleMessage(ctx, "/sessions"); err != nil {
		t.Fatalf("/sessions: %v", err)
	}
	out := p.out.String()
	if !strings.Contains(out, "sessions:") {
		t.Fatalf("/sessions output = %q", out)
	}
	if strings.Contains(out, "platform: cli/local") && strings.Contains(out, "regular") == false {
		t.Fatalf("regular user should only see own sessions: %q", out)
	}
}

func TestRegularUserCannotUseSuperadminSlashCommands(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"ok"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "cli", PlatformUserID: "regular", ScopeID: "regular"})

	for _, cmd := range []string{"/model", "/requests", "/audit", "/log", "/tools", "/clean"} {
		p.out.Reset()
		if err := a.HandleMessage(ctx, cmd); err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		if !strings.Contains(p.out.String(), "需要超级管理员权限") {
			t.Fatalf("%s should be denied for regular user, got %q", cmd, p.out.String())
		}
	}
}

func TestRegularUserCanCallOwnerScopedTool(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "resident_memory_core", Args: `{"content":"我喜欢咖啡"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "已记下"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	if err := registry.Register(tool.NewDiscoverTool(registry)); err != nil {
		t.Fatal(err)
	}
	memStore := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	for _, tl := range builtin.NewResidentMemoryTools(memStore) {
		if err := registry.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	a.SetToolRuntime(registry, nil)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "cli", PlatformUserID: "regular", ScopeID: "regular"})

	if err := a.HandleMessage(ctx, "更新我的核心记忆为：我喜欢咖啡"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !strings.Contains(p.out.String(), "已记下") {
		t.Fatalf("owner-scoped tool should succeed for regular user, output = %q", p.out.String())
	}
	scope := resident.ActorScope(security.Actor{ID: "cli:regular", Platform: "cli", PlatformUserID: "regular", Role: security.RoleUser})
	mem, err := memStore.Read(ctx, scope)
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	if mem.Core != "我喜欢咖啡" {
		t.Fatalf("core memory = %q, want 我喜欢咖啡", mem.Core)
	}
}

func TestRegularUserCannotCallSuperadminOnlyTool(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "long_memory_write", Args: `{"category":"x","title":"t","summary":"s","content":"c"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "fallback"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	if err := registry.Register(tool.NewDiscoverTool(registry)); err != nil {
		t.Fatal(err)
	}
	for _, tl := range builtin.NewLongMemoryTools(t.TempDir()) {
		if err := registry.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	a.SetToolRuntime(registry, nil)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "cli", PlatformUserID: "regular", ScopeID: "regular"})

	if err := a.HandleMessage(ctx, "写长期记忆"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) < 2 {
		t.Fatalf("expected followup LLM request with tool result, got %d requests", len(requests))
	}
	followup := requests[1]
	var toolMsg string
	for _, m := range followup.Messages {
		if m.Role == llm.RoleTool && m.Name == "long_memory_write" {
			toolMsg = llm.SegmentsContentText(m.Segments)
		}
	}
	if toolMsg == "" {
		t.Fatalf("missing tool result message in followup: %#v", followup.Messages)
	}
	if !strings.Contains(toolMsg, "superadmin") {
		t.Fatalf("superadmin-only tool should be denied for regular user, tool message = %q", toolMsg)
	}
}

func TestSuperadminStillGetsConfirmationForHighRiskOwnerScopedTool(t *testing.T) {
	policy := security.DefaultPolicy()
	if !policy.NeedsToolConfirmation(security.Actor{Role: security.RoleSuperadmin}, security.RiskHigh) {
		t.Fatalf("superadmin should need confirmation for high risk")
	}
	if policy.NeedsToolConfirmation(security.Actor{Role: security.RoleUser}, security.RiskHigh) {
		t.Fatalf("regular user should never be prompted")
	}
	if !policy.CanUseTool(security.Actor{Role: security.RoleUser}, security.RiskHigh, true) {
		t.Fatalf("regular user should be allowed for owner-scoped high risk")
	}
	if policy.CanUseTool(security.Actor{Role: security.RoleUser}, security.RiskHigh, false) {
		t.Fatalf("regular user should be denied for non-owner high risk")
	}
}
