package agent

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePlatform struct {
	out         strings.Builder
	preview     strings.Builder
	reasoning   strings.Builder
	statusCount int
	lastStatus  runtimestatus.Snapshot
}

type fakeHookRouter struct {
	event  hook.Event
	routed bool
}

func (r *fakeHookRouter) Cancel(hook.Event) bool { return false }

func (r *fakeHookRouter) RouteHookID(hook.Event) string { return "" }

func (r *fakeHookRouter) Route(_ context.Context, event hook.Event) (hook.Event, bool, error) {
	if !r.routed {
		return event, false, nil
	}
	event.Outputs = append(event.Outputs, r.event.Outputs...)
	event.Control = r.event.Control
	return event, true, nil
}

func (p *fakePlatform) Name() string { return "cli" }

func (p *fakePlatform) Run(context.Context, platform.PlatformHandler) error { return nil }

func (p *fakePlatform) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	p.out.WriteString(delivery.FallbackOutput(outputs).Text)
	return delivery.Receipt{}, nil
}

func (p *fakePlatform) SendNotice(_ context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	text := delivery.FallbackOutput(outputs).Text
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
	store, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestNewTestStoreUsesIndependentMemoryDatabase(t *testing.T) {
	ctx := context.Background()
	first := newTestStore(t)
	second := newTestStore(t)
	session := &storage.Session{OwnerID: "u1", Platform: "cli", PlatformScopeID: "local", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive}
	if err := first.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := second.Sessions().Get(ctx, session.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("second store Get error = %v, want not found", err)
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

func toolNames(schemas []llm.ToolSchema) string {

	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema.Function.Name)
	}
	return strings.Join(names, ",")
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

type agentLazyDetailTool struct {
	agentDetailTool
	err error
}

func (t agentLazyDetailTool) LoadDetail() (tool.DetailBlock, error) {
	if t.err != nil {
		return tool.DetailBlock{}, t.err
	}
	return t.DetailBlock(), nil
}

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
