package execution

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	agenthooks "elbot/internal/agent/hooks"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

var testProviderFiles sync.Map

func newTestModels(t *testing.T, opts modelmgr.Options) *modelmgr.Service {
	t.Helper()
	if opts.Clients == nil {
		opts.Clients = map[string]llm.Client{}
	}
	for name, provider := range opts.Providers {
		if opts.Clients[name] == nil && provider.BaseURL != "" {
			client, err := chatcompletions.New(provider.BaseURL, provider.APIKey, provider.ExtraPayload, nil, chatcompletions.RequestOptions{Proxy: provider.Proxy})
			if err != nil {
				t.Fatalf("create test provider %s: %v", name, err)
			}
			opts.Clients[name] = client
		}
	}

	file := &config.ProviderFile{Providers: opts.Providers, ModelMetadata: opts.Metadata}
	if opts.LoadProviders == nil {
		opts.LoadProviders = func(context.Context) (config.ProviderFile, error) { return *file, nil }
	}
	if opts.BuildClient == nil {
		opts.BuildClient = func(name string, _ config.ProviderConfig) (llm.Client, error) { return opts.Clients[name], nil }
	}
	models, err := modelmgr.New(opts)
	if err != nil {
		t.Fatalf("modelmgr.New: %v", err)
	}
	testProviderFiles.Store(models, file)
	t.Cleanup(func() { testProviderFiles.Delete(models); _ = models.Close(context.Background()) })
	return models
}

type fakePlatform struct {
	out         safeStringBuilder
	preview     safeStringBuilder
	reasoning   safeStringBuilder
	statusMu    sync.Mutex
	statusCount int
	lastStatus  runtimestatus.Snapshot
}

type safeStringBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *safeStringBuilder) WriteString(text string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.WriteString(text)
}

func (b *safeStringBuilder) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (b *safeStringBuilder) Reset() {
	b.mu.Lock()
	b.b.Reset()
	b.mu.Unlock()
}

func (p *fakePlatform) Name() string { return "cli" }

func (p *fakePlatform) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	p.out.WriteString(delivery.FallbackOutput(outputs).Text)
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func (p *fakePlatform) SendTo(_ context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	outputs := noticeOutputs
	text := delivery.FallbackOutput(outputs).Text
	p.out.WriteString(text)
	p.preview.WriteString(text)
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func (p *fakePlatform) SetRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) error {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	p.statusCount++
	p.lastStatus = snapshot
	return nil
}

func (p *fakePlatform) statusSnapshot() (int, runtimestatus.Snapshot) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	return p.statusCount, p.lastStatus
}

type fakeLLMBlock struct {
	ignoreCancellation bool
	started            chan struct{}
	release            chan struct{}
}

type fakeLLM struct {
	models        []string
	replies       []string
	chunks        [][]chatcompletions.Chunk
	titleReplies  []string
	chatBlocks    []fakeLLMBlock
	requests      []chatcompletions.Request
	requestNotify chan struct{}
	mu            sync.Mutex
}

func (f *fakeLLM) Stream(ctx context.Context, req chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
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
		ch := make(chan chatcompletions.Chunk, len(chunks))
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
	ch := make(chan chatcompletions.Chunk, 1)
	if reply == "__ERR__" {
		ch <- chatcompletions.Chunk{Error: fmt.Errorf("fake stream error")}
	} else {
		ch <- chatcompletions.Chunk{DeltaContent: reply}
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
	if block.ignoreCancellation {
		<-block.release
		return nil
	}
	select {
	case <-block.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func fakeLLMErrorStream(err error) <-chan chatcompletions.Chunk {
	ch := make(chan chatcompletions.Chunk, 1)
	ch <- chatcompletions.Chunk{Error: err}
	close(ch)
	return ch
}

func (f *fakeLLM) ListModels(context.Context) ([]string, error) { return f.models, nil }

func (f *fakeLLM) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeLLM) chatRequests() []chatcompletions.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []chatcompletions.Request{}
	for _, req := range f.requests {
		if !isTitleRequest(req) {
			out = append(out, req)
		}
	}
	return out
}

func isTitleRequest(req chatcompletions.Request) bool {
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

var sharedTestStore *sqlite.Store

var sharedTestDB *sql.DB

func TestMain(m *testing.M) {
	const dsn = "file:elbot-agent-tests?mode=memory&cache=shared"
	var err error
	sharedTestStore, err = sqlite.New(context.Background(), dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sharedTestDB, err = sql.Open("sqlite", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sharedTestDB.SetMaxOpenConns(1)
	if _, err := sharedTestDB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if err := sharedTestStore.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	if err := sharedTestDB.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

// Agent tests run serially. Register this before constructing services so their
// cleanup stops background work before the shared database is cleared.
func newTestStore(t *testing.T) storage.Store {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		tx, err := sharedTestDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		// Foreign-key cascades clear child rows; keep tables, triggers and versions.
		if _, err := tx.ExecContext(ctx, `
DELETE FROM sessions;
DELETE FROM cron_jobs;
DELETE FROM elnis_events;
DELETE FROM media_outputs;
DELETE FROM media_history;
DELETE FROM media_references;
DELETE FROM media;
`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	})
	return sharedTestStore
}

func (f *fakeLLM) GenerateText(ctx context.Context, req llm.TextRequest) (llm.TextResult, error) {
	messages := []llm.LLMMessage{}
	if req.Instructions != "" {
		messages = append(messages, llm.LLMMessage{Role: llm.RoleSystem, Segments: llm.TextSegments(req.Instructions)})
	}
	messages = append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments(req.Input)})
	chunks, err := f.Stream(ctx, chatcompletions.Request{Model: req.Model, Messages: messages, MaxTokens: req.MaxOutputTokens, ExtraBody: req.ExtraBody})
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

func newTestHookBridge(t *testing.T, points agenthooks.Points) *agenthooks.HookBridge {
	t.Helper()
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{})
	if _, err := bridge.Register(points); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })
	return bridge
}
