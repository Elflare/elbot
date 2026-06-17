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
	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/hook"
	hookbuiltin "elbot/internal/hook/builtin"
	"elbot/internal/llm"
	"elbot/internal/output"
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

func (p *fakePlatform) SendChat(ctx context.Context, out output.Output) (platform.Receipt, error) {
	p.out.WriteString(output.FallbackText(out))
	return platform.Receipt{}, nil
}

func (p *fakePlatform) SendNotice(ctx context.Context, target output.Target, out output.Output) (platform.Receipt, error) {
	text := output.FallbackText(out)
	p.out.WriteString(text)
	p.preview.WriteString(text)
	return platform.Receipt{}, nil
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

func (p *fakeStreamingPlatform) StartStream(ctx context.Context) (platform.MessageStream, error) {
	return &p.stream, nil
}

func (s *fakeMessageStream) Append(ctx context.Context, text string) error {
	s.appends = append(s.appends, text)
	return nil
}

func (s *fakeMessageStream) Replace(ctx context.Context, text string) (platform.Receipt, error) {
	s.replaces = append(s.replaces, text)
	return platform.Receipt{}, nil
}

func (s *fakeMessageStream) Finish(ctx context.Context) (platform.Receipt, error) {
	s.finished++
	return platform.Receipt{}, nil
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

func TestHandleMessageSendsLLMErrorToPlatform(t *testing.T) {
	p := &fakePlatform{}
	a := New(p, &fakeLLM{replies: []string{"__ERR__"}}, "test-model", config.ProviderConfig{}, newTestStore(t))

	err := a.HandleMessage(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "fake stream error") {
		t.Fatalf("HandleMessage err = %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "请求失败：") || !strings.Contains(got, "fake stream error") {
		t.Fatalf("platform output missing error: %q", got)
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
			output.Text("immediate"),
			output.WithDeliveryTiming(output.Text("after"), output.DeliveryAfterAssistant),
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
	if !strings.Contains(got, "default:\n") || !strings.Contains(got, "* [2] beta (chat, work, compact)") {
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

func TestAgentRefreshesRuntimeStateFromStateFile(t *testing.T) {
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

	if err := config.SaveState(statePath, config.StateConfig{ModeModels: modeModels}); err != nil {
		t.Fatalf("save initial state: %v", err)
	}
	a.stateModTime = initialStateModTime(statePath)
	time.Sleep(10 * time.Millisecond)
	if err := config.SaveState(statePath, config.StateConfig{
		ModeModels: map[string]config.ModelSelection{
			storage.SessionModeWork: {Provider: "zhipu", Model: "glm-4-flash"},
			storage.SessionModeChat: {Provider: "deepseek", Model: "deepseek-chat"},
		},
		CompactModel: config.ModelSelection{Provider: "zhipu", Model: "glm-4-flash"},
		NamingModel:  config.ModelSelection{Provider: "zhipu", Model: "glm-4-flash"},
	}); err != nil {
		t.Fatalf("save updated state: %v", err)
	}

	if err := a.HandleMessage(context.Background(), "/status"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if got := a.CurrentModelForMode(storage.SessionModeWork); got.Provider != "zhipu" || got.Model != "glm-4-flash" {
		t.Fatalf("work model = %s/%s", got.Provider, got.Model)
	}
	if got := a.CurrentCompactModel(); got.Provider != "zhipu" || got.Model != "glm-4-flash" {
		t.Fatalf("compact model = %s/%s", got.Provider, got.Model)
	}
	if got := a.CurrentNamingModel(); got.Provider != "zhipu" || got.Model != "glm-4-flash" {
		t.Fatalf("naming model = %s/%s", got.Provider, got.Model)
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

	a.mapSentAssistantMessage(ctx, session.ID, assistant.ID, platform.Receipt{PlatformMessageIDs: []string{"101", "", "102"}})
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

func TestNonSuperadminIdleTTLExpiresCurrentSession(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"fresh reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetNonSuperadminIdleTTLMinutes(10)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"})

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
	if _, err := store.Sessions().Get(ctx, oldSession.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("old session err = %v, want not found", err)
	}
	current, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if current.ID == oldSession.ID {
		t.Fatal("current session was not replaced")
	}
	messages, err := store.Messages().ListBySession(ctx, current.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Content != "hello again" || messages[1].Content != "fresh reply" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestSuperadminIdleTTLKeepsCurrentSession(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"same reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"qq": {"1"}}))
	a.SetNonSuperadminIdleTTLMinutes(10)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"})

	oldSession, err := a.sessions.Create(ctx, a.scope(ctx), "old")
	if err != nil {
		t.Fatalf("create old session: %v", err)
	}
	oldSession.UpdatedAt = time.Now().Add(-11 * time.Minute)
	if err := store.Sessions().Update(ctx, oldSession); err != nil {
		t.Fatalf("age old session: %v", err)
	}

	if err := a.HandleMessage(ctx, "hello admin"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	current, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if current.ID != oldSession.ID {
		t.Fatalf("current session = %s, want %s", current.ID, oldSession.ID)
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

	if err := a.HandleMessage(context.Background(), "查资料 @tool:web_search"); err != nil {
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
	if len(messages) == 0 || messages[0].Content != "查资料" || strings.Contains(messages[0].Content, "@tool:web_search") {
		t.Fatalf("stored user message = %#v", messages)
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
	_ = registry.Register(agentDetailTool{name: "skill_web", source: tool.SourceSkillPy, detail: "# skill", activate: []string{"python_skill_run"}})
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

func TestToolDirectiveInvalidStaysAsText(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "hello @tool:nope"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	latest := requests[0].Messages[len(requests[0].Messages)-1]
	if got := llm.SegmentsContentText(latest.Segments); got != "hello @tool:nope" {
		t.Fatalf("latest user content = %q", got)
	}
	if !strings.Contains(p.out.String(), "未找到或不可用的工具：nope") {
		t.Fatalf("missing invalid tool notice: %q", p.out.String())
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
	_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillPy, detail: "# DOCX", activate: []string{"python_skill_run"}})
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
func (t agentDetailTool) Detail() string          { return t.detail }
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
	rootDir := filepath.Join(configDir, "emotions")
	if err := os.MkdirAll(filepath.Join(rootDir, "微笑"), 0o755); err != nil {
		t.Fatalf("mkdir emoticon dir: %v", err)
	}
	cfg := fmt.Sprintf("root_dir = %q\ntiming = %q\n", rootDir, output.DeliveryAfterAssistant)
	if err := os.WriteFile(filepath.Join(configDir, "emoticon.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write emoticon config: %v", err)
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
	if !strings.Contains(p.out.String(), fullArgs) {
		t.Fatalf("detail did not include full args: %q", p.out.String())
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
	a.promptBuilder.Soul = staticSoulProvider{Prompt: "SOUL ONLY"}
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

func TestRunCronMessagePreloadsToolListNames(t *testing.T) {
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
	if toolNames(requests[0].Tools) != "discover_tool,web_extract,web_search" {
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
	text, err := a.latestAssistantText(ctx, cronSession.ID)
	if err != nil {
		t.Fatalf("latestAssistantText: %v", err)
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
	rootDir := filepath.Join(configDir, "emotions")
	if err := os.MkdirAll(filepath.Join(rootDir, "微笑"), 0o755); err != nil {
		t.Fatalf("mkdir emoticon dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "emoticon.toml"), []byte("root_dir = "+fmt.Sprintf("%q", rootDir)), 0o644); err != nil {
		t.Fatalf("write emoticon config: %v", err)
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
