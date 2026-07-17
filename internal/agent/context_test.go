package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func TestCompactMessagesFiltersToolResultsAndFailedCalls(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	session := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", Title: "compact"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, record := range []*storage.ToolCallRecord{
		{SessionID: session.ID, ToolCallID: "ok", ToolName: "shell", ActorID: "u", RiskLevel: "low", Success: true},
		{SessionID: session.ID, ToolCallID: "failed", ToolName: "web", ActorID: "u", RiskLevel: "low", Success: false, Error: "boom"},
	} {
		if err := store.ToolCalls().Create(ctx, record); err != nil {
			t.Fatalf("create tool call: %v", err)
		}
	}
	calls := []llm.ToolCallRequest{
		{ID: "ok", Name: "shell", Arguments: `{"command":"go test ./..."}`},
		{ID: "failed", Name: "web", Arguments: `{"q":"failed"}`},
		{ID: "missing", Name: "shell", Arguments: `{"command":"missing"}`},
	}
	toolCall := toolCallStorageMessage(session.ID, "C", "C", calls)
	runtime := contextRuntimeState{store: store}
	messages, err := runtime.compactMessages(ctx, &contextmgr.LoadedContext{
		Summary: &storage.ContextSummary{Summary: "I"},
		Messages: []storage.Message{
			{Role: storage.RoleUser, Content: "J"},
			toolCall,
			{Role: storage.RoleTool, Content: "secret tool result", ToolCallID: "ok"},
			{Role: storage.RoleAssistant, Content: "H"},
		},
	})
	if err != nil {
		t.Fatalf("compact messages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].Role != storage.RoleUser || messages[0].Content != "I\n\n当前用户输入：\nJ" {
		t.Fatalf("first message = %#v", messages[0])
	}
	if len(messages[1].ToolCalls) != 1 || messages[1].ToolCalls[0].Name != "shell" || !strings.Contains(messages[1].ToolCalls[0].Arguments, "go test") {
		t.Fatalf("tool calls = %#v", messages[1].ToolCalls)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "secret tool result") {
			t.Fatalf("tool result leaked into compact messages: %#v", messages)
		}
	}
}

func TestAutoCompactCreatesStableFirstUserContext(t *testing.T) {
	ctx := context.Background()
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"K", "answer J", "answer L", "K2", "answer M"}}
	store := newTestStore(t)
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	source, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "compact"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, message := range []*storage.Message{
		{SessionID: source.ID, Role: storage.RoleUser, Content: "B"},
		{SessionID: source.ID, Role: storage.RoleAssistant, Content: "H"},
	} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	a.SetContextOptions(config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.8}, config.ModelMetadataConfig{DefaultContextWindow: 100}, nil, config.ModelSelection{})
	a.recordUsage(source.ID, &llm.Usage{TotalTokens: 80})

	if err := a.HandleMessage(ctx, "J"); err != nil {
		t.Fatalf("first message after compact: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	compactPayload := llm.SegmentsContentText(requests[0].Messages[1].Segments)
	for _, want := range []string{"上下文内容：", "user: B", "assistant: H", "用户原话：\n1. B"} {
		if !strings.Contains(compactPayload, want) {
			t.Fatalf("compact payload missing %q:\n%s", want, compactPayload)
		}
	}
	compacted, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		t.Fatalf("current compacted session: %v", err)
	}
	if compacted.ID == source.ID || compacted.ParentSessionID != "" || compacted.ForkFromMessageID != "" {
		t.Fatalf("compacted session is not independent: %#v", compacted)
	}
	if compacted.Title != "compact compacted-1" {
		t.Fatalf("compacted title = %q", compacted.Title)
	}
	firstUser := llm.SegmentsContentText(requests[1].Messages[1].Segments)
	for _, want := range []string{"K", "以下是用户原话：\n1. B", "当前用户输入：\nJ"} {
		if !strings.Contains(firstUser, want) {
			t.Fatalf("first user missing %q:\n%s", want, firstUser)
		}
	}

	if err := a.HandleMessage(ctx, "L"); err != nil {
		t.Fatalf("second message after compact: %v", err)
	}
	requests = f.chatRequests()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(requests))
	}
	secondTurn := requests[2].Messages
	if got := llm.SegmentsContentText(secondTurn[1].Segments); got != firstUser {
		t.Fatalf("first user moved or changed:\nfirst: %s\nsecond: %s", firstUser, got)
	}
	if got := llm.SegmentsContentText(secondTurn[len(secondTurn)-1].Segments); got != "L" {
		t.Fatalf("latest user = %q", got)
	}

	oldMessages, err := store.Messages().ListBySession(ctx, source.ID)
	if err != nil {
		t.Fatalf("list old messages: %v", err)
	}
	if len(oldMessages) != 2 {
		t.Fatalf("old session changed: %#v", oldMessages)
	}
	newMessages, err := store.Messages().ListBySession(ctx, compacted.ID)
	if err != nil {
		t.Fatalf("list compacted messages: %v", err)
	}
	if len(newMessages) < 1 || newMessages[0].Role != storage.RoleUser || newMessages[0].Content != firstUser {
		t.Fatalf("first compacted user message = %#v, want %q", newMessages, firstUser)
	}
	compacted, err = store.Sessions().Get(ctx, compacted.ID)
	if err != nil {
		t.Fatalf("reload compacted session: %v", err)
	}
	compactMetadata := decodeSessionMetadata(compacted.Metadata).ContextCompact
	if compactMetadata == nil || compactMetadata.Pending || compactMetadata.Generation != 1 || compactMetadata.SourceSessionID != source.ID {
		t.Fatalf("compact metadata = %#v", compactMetadata)
	}

	a.recordUsage(compacted.ID, &llm.Usage{TotalTokens: 80})
	if err := a.HandleMessage(ctx, "M"); err != nil {
		t.Fatalf("message after repeated compact: %v", err)
	}
	requests = f.chatRequests()
	if len(requests) != 5 {
		t.Fatalf("requests after repeated compact = %d, want 5", len(requests))
	}
	repeatedPayload := llm.SegmentsContentText(requests[3].Messages[1].Segments)
	for _, want := range []string{"user: K", "以下是用户原话：", "当前用户输入：\nJ", firstUser, "2. L"} {
		if !strings.Contains(repeatedPayload, want) {
			t.Fatalf("repeated compact payload missing %q:\n%s", want, repeatedPayload)
		}
	}
	if strings.Contains(repeatedPayload, "已有摘要") || strings.Contains(repeatedPayload, "已有较早摘要") {
		t.Fatalf("repeated compact used a special previous-summary field:\n%s", repeatedPayload)
	}
	repeated, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		t.Fatalf("current repeated session: %v", err)
	}
	if repeated.ID == compacted.ID || repeated.ParentSessionID != "" || repeated.Title != "compact compacted-2" {
		t.Fatalf("repeated compact session = %#v", repeated)
	}
	mainAfterRepeated := llm.SegmentsContentText(requests[4].Messages[1].Segments)
	for _, want := range []string{"K2", "以下是用户原话：", firstUser, "当前用户输入：\nM"} {
		if !strings.Contains(mainAfterRepeated, want) {
			t.Fatalf("main user after repeated compact missing %q: %q", want, mainAfterRepeated)
		}
	}
	if _, err := store.ContextSummaries().LatestBySession(ctx, repeated.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("new compact flow wrote a context summary: %v", err)
	}
}

func TestManualCompactDefersSeedUntilNextUserMessage(t *testing.T) {
	ctx := context.Background()
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"K", "answer J"}}
	store := newTestStore(t)
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	old, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "manual"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, message := range []*storage.Message{{SessionID: old.ID, Role: storage.RoleUser, Content: "B"}, {SessionID: old.ID, Role: storage.RoleAssistant, Content: "H"}} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	if err := a.HandleMessage(ctx, "/compact"); err != nil {
		t.Fatalf("manual compact: %v", err)
	}
	next, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if seed := pendingContextCompact(next); next.ID == old.ID || seed == nil || !seed.Pending {
		t.Fatalf("pending compacted session = %#v", next)
	}
	if messages, err := store.Messages().ListBySession(ctx, next.ID); err != nil || len(messages) != 0 {
		t.Fatalf("messages before J = %#v, err = %v", messages, err)
	}
	if err := a.HandleMessage(ctx, "J"); err != nil {
		t.Fatalf("first user message: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	firstUser := llm.SegmentsContentText(requests[1].Messages[1].Segments)
	for _, want := range []string{"K", "1. B", "当前用户输入：\nJ"} {
		if !strings.Contains(firstUser, want) {
			t.Fatalf("first user missing %q: %q", want, firstUser)
		}
	}
	messages, err := store.Messages().ListBySession(ctx, next.ID)
	if err != nil || len(messages) < 1 || messages[0].Content != firstUser {
		t.Fatalf("persisted first user = %#v, err = %v", messages, err)
	}
	latest, _ := store.Sessions().Get(ctx, next.ID)
	if compact := decodeSessionMetadata(latest.Metadata).ContextCompact; compact == nil || compact.Pending {
		t.Fatalf("compact seed was not consumed: %#v", compact)
	}
	if got := p.out.String(); !strings.Contains(got, "new session: "+next.ID) {
		t.Fatalf("completion output = %q", got)
	}
}

func TestCompactBlocksSessionChangesAndStopCancels(t *testing.T) {
	ctx := context.Background()
	p := &fakePlatform{}
	started := make(chan struct{})
	f := &fakeLLM{replies: []string{"K"}, chatBlocks: []fakeLLMBlock{{started: started, release: make(chan struct{})}}}
	store := newTestStore(t)
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	source, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "compact"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, message := range []*storage.Message{
		{SessionID: source.ID, Role: storage.RoleUser, Content: "B"},
		{SessionID: source.ID, Role: storage.RoleAssistant, Content: "H"},
	} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	a.SetContextOptions(config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.8}, config.ModelMetadataConfig{DefaultContextWindow: 100}, nil, config.ModelSelection{})
	a.recordUsage(source.ID, &llm.Usage{TotalTokens: 80})

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "/compact") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("compact model did not start")
	}
	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatalf("blocked /new: %v", err)
	}
	if err := a.HandleMessage(ctx, "/delete "+source.ID+" --confirm"); err != nil {
		t.Fatalf("blocked /delete: %v", err)
	}
	current, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil || current.ID != source.ID {
		t.Fatalf("current session = %#v, err = %v", current, err)
	}
	if _, err := store.Sessions().Get(ctx, source.ID); err != nil {
		t.Fatalf("source session was deleted: %v", err)
	}
	if err := a.HandleMessage(ctx, "/stop"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("compact returned: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("compact did not stop")
	}
	if got := a.turns.Snapshot(source.ID).Phase; got != turn.PhaseIdle {
		t.Fatalf("turn phase = %s", got)
	}
	if got := len(a.requests.ListBySession(source.ID)); got != 0 {
		t.Fatalf("active requests = %d", got)
	}
	if _, err := store.ContextSummaries().LatestBySession(ctx, source.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("summary after cancel err = %v", err)
	}
	if !a.shouldCompact(ctx, source, a.modelSelectionForTurn(ctx, source)) {
		t.Fatal("usage no longer triggers compact after cancellation")
	}
	if got := p.out.String(); !strings.Contains(got, "暂不执行 /new") || !strings.Contains(got, "暂不执行 /delete") || !strings.Contains(got, "stopped 1 request") {
		t.Fatalf("output = %q", got)
	}
}

func TestAutoCompactFailureKeepsSourceSession(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	a := New(&fakePlatform{}, &fakeLLM{replies: []string{"__ERR__"}}, "test-model", config.ProviderConfig{}, store)
	source, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "failure"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, message := range []*storage.Message{{SessionID: source.ID, Role: storage.RoleUser, Content: "B"}, {SessionID: source.ID, Role: storage.RoleAssistant, Content: "H"}} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	a.SetContextOptions(config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.8}, config.ModelMetadataConfig{DefaultContextWindow: 100}, nil, config.ModelSelection{})
	a.recordUsage(source.ID, &llm.Usage{TotalTokens: 80})
	if err := a.HandleMessage(ctx, "J"); err == nil {
		t.Fatal("auto compact unexpectedly succeeded")
	}
	current, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil || current.ID != source.ID {
		t.Fatalf("current session = %#v, err = %v", current, err)
	}
	messages, err := store.Messages().ListBySession(ctx, source.ID)
	if err != nil || len(messages) != 2 {
		t.Fatalf("source messages = %#v, err = %v", messages, err)
	}
	sessions, err := a.sessions.List(ctx, a.scope(ctx), "", 10)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %#v, err = %v", sessions, err)
	}
}

func TestCompactBlockedCommandSet(t *testing.T) {
	for _, effect := range []command.SessionEffect{command.SessionEffectSwitchCurrent, command.SessionEffectMutate, command.SessionEffectSwitchCurrent | command.SessionEffectMutate} {
		if !blocksDuringCompact(effect) {
			t.Fatalf("effect %d should be blocked", effect)
		}
	}
	if blocksDuringCompact(command.SessionEffectNone) {
		t.Fatal("commands without session effects should remain available")
	}
}

func TestModelSwitchDuringTurnAppliesOnNextTurn(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	f := &fakeLLM{
		models:     []string{"old", "new"},
		replies:    []string{"first answer", "second answer"},
		chatBlocks: []fakeLLMBlock{{started: started, release: release}},
	}
	a := New(&fakePlatform{}, f, "old", config.ProviderConfig{Models: []string{"old", "new"}}, newTestStore(t))

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "first") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first model request did not start")
	}
	if err := a.HandleMessage(ctx, "/model new"); err != nil {
		t.Fatalf("switch model: %v", err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first turn: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first turn did not finish")
	}
	if err := a.HandleMessage(ctx, "second"); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	if requests[0].Model != "old" || requests[1].Model != "new" {
		t.Fatalf("request models = %q, %q", requests[0].Model, requests[1].Model)
	}
}

func TestModelSwitchReevaluatesCompactWindow(t *testing.T) {
	ctx := context.Background()
	provider := config.ProviderConfig{
		Models: []string{"small", "large"},
		ModelConfigs: map[string]config.ModelConfig{
			"small": {ContextWindow: 100},
			"large": {ContextWindow: 1000},
		},
	}
	a := New(&fakePlatform{}, &fakeLLM{models: []string{"small", "large"}}, "small", provider, newTestStore(t))
	a.SetContextOptions(config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.8}, config.ModelMetadataConfig{DefaultContextWindow: 100}, map[string]config.ProviderConfig{"default": provider}, config.ModelSelection{})
	current, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "model window"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	a.recordUsage(current.ID, &llm.Usage{TotalTokens: 80})
	if !a.shouldCompact(ctx, current, a.modelSelectionForTurn(ctx, current)) {
		t.Fatal("small model did not trigger compact")
	}
	if _, err := a.SelectModel(ctx, "large"); err != nil {
		t.Fatalf("select large model: %v", err)
	}
	if a.shouldCompact(ctx, current, a.modelSelectionForTurn(ctx, current)) {
		t.Fatal("large model reused the old model compact decision")
	}
}
