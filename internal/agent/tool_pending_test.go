package agent

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"errors"
	"strings"
	"testing"
	"time"
)

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
	manager := hook.NewManager()
	var pendingPlatformText string
	if err := manager.Register(hook.Registration{Point: hook.PointLLMRequestPrepared, Name: "test.pending", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		for i := len(event.LLM.Messages) - 1; i >= 0; i-- {
			if event.LLM.Messages[i].Role == llm.RoleUser && len(event.LLM.Messages[i].Segments) > 0 {
				event.LLM.Messages[i].Segments[0].Text = "不应写回历史"
				break
			}
		}
		if event.Message.Role == string(llm.RoleUser) {
			pendingPlatformText = event.Message.PlatformText
			event.Message.Segments = []llm.MessageSegment{
				{Type: llm.SegmentText, Text: "这是猫"},
				{Type: llm.SegmentImage, URL: "https://example.com/cat.png", Name: "cat.png"},
			}
		}
		return event, nil
	})}); err != nil {
		t.Fatalf("Register request hook: %v", err)
	}
	a.SetHookManager(manager)
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
		if msg.Role == llm.RoleUser && strings.Contains(llm.SegmentsContentText(msg.Segments), "这是猫") && len(msg.Segments) == 2 && msg.Segments[1].Type == llm.SegmentImage {
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
		if msg.Role == storage.RoleUser && strings.Contains(msg.Content, "这是猫") && strings.Contains(msg.Segments, `"type":"image"`) {
			persistedPending = true
		}
	}
	if !persistedPending {
		t.Fatalf("pending input was not persisted: %#v", messages)
	}
	if pendingPlatformText != "这是追问" {
		t.Fatalf("pending platform text = %q", pendingPlatformText)
	}
	if got := llm.LatestUserSegmentTextOnly(requests[0].Messages); got != "先查" {
		t.Fatalf("first request user = %q, request hook modified current turn input", got)
	}
	if !strings.Contains(p.out.String(), "已追加") {
		t.Fatalf("missing pending acknowledgement: %q", p.out.String())
	}
}

func TestToolPhasePendingInputPersistsWhenRequestHookFails(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "slow", Args: `{}`}}, FinishReason: "tool_calls"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMRequestPrepared, Name: "test.fail-pending", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		if event.Message.Role == string(llm.RoleUser) {
			return event, errors.New("request hook failed")
		}
		return event, nil
	})}); err != nil {
		t.Fatalf("Register request hook: %v", err)
	}
	a.SetHookManager(manager)
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
	if err := a.HandleMessage(ctx, "即使 Hook 失败也要保留"); err != nil {
		t.Fatalf("pending HandleMessage: %v", err)
	}
	close(release)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "request hook failed") {
			t.Fatalf("HandleMessage error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool flow did not finish")
	}

	messages, err := store.Messages().ListBySession(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range messages {
		if msg.Role == storage.RoleUser && strings.Contains(msg.Content, "即使 Hook 失败也要保留") {
			return
		}
	}
	t.Fatalf("pending input was lost after request hook failure: %#v", messages)
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
	manager := hook.NewManager()
	completedCalls := 0
	if err := manager.Register(hook.Registration{Point: hook.PointToolCallCompleted, Name: "test.cancel-result", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		completedCalls++
		if event.Tool.Error != nil {
			event.Message.Segments = llm.TextSegments("HOOK_CANCELED")
		}
		return event, nil
	})}); err != nil {
		t.Fatalf("Register completed hook: %v", err)
	}
	a.SetHookManager(manager)
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
		if msg.Role == llm.RoleTool && msg.ToolCallID == "call_1" && strings.Contains(llm.SegmentsContentText(msg.Segments), "HOOK_CANCELED") {
			canceledTool = true
		}
	}
	if !canceledTool {
		t.Fatalf("followup request missing canceled tool message: %#v", requests[1].Messages)
	}
	if completedCalls != 1 {
		t.Fatalf("completed hook calls = %d, want 1", completedCalls)
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

func TestToolPhasePendingInputDuringFinalLLMStartsNewTurn(t *testing.T) {
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
	if err := a.HandleMessage(ctx, "再补一句"); err != nil {
		t.Fatalf("second pending HandleMessage: %v", err)
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
	for _, msg := range requests[1].Messages {
		content := llm.SegmentsContentText(msg.Segments)
		if strings.Contains(content, "收尾前补一句") || strings.Contains(content, "再补一句") {
			t.Fatalf("final request unexpectedly included late pending input: %#v", requests[1].Messages)
		}
	}
	retry := requests[2]
	var sawAssistant, sawPending bool
	mergedPending := "补充信息：\n1. 收尾前补一句\n2. 再补一句"
	for _, msg := range retry.Messages {
		if msg.Role == llm.RoleAssistant && llm.SegmentsContentText(msg.Segments) == "almost final" {
			sawAssistant = true
		}
		if msg.Role == llm.RoleUser && llm.SegmentsContentText(msg.Segments) == mergedPending {
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
	assistantIdx, pendingIdx, followupIdx := -1, -1, -1
	for i, msg := range messages {
		switch {
		case msg.Role == storage.RoleAssistant && msg.Content == "almost final":
			assistantIdx = i
		case msg.Role == storage.RoleUser && msg.Content == mergedPending:
			pendingIdx = i
		case msg.Role == storage.RoleAssistant && msg.Content == "final with pending":
			followupIdx = i
		}
	}
	if assistantIdx < 0 || pendingIdx < 0 || followupIdx < 0 || !(assistantIdx < pendingIdx && pendingIdx < followupIdx) {
		t.Fatalf("unexpected automatic followup message order: %#v", messages)
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
