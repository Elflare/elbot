package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
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
	a := &Agent{store: store}
	messages, err := a.compactMessages(ctx, &contextmgr.LoadedContext{
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
	session, err := a.sessions.Create(ctx, a.scope(ctx), "compact")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, message := range []*storage.Message{
		{SessionID: session.ID, Role: storage.RoleUser, Content: "B"},
		{SessionID: session.ID, Role: storage.RoleAssistant, Content: "H"},
	} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	a.usageMu.Lock()
	a.pendingCompact[session.ID] = true
	a.usageMu.Unlock()

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

	summary, err := store.ContextSummaries().LatestBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("load summary: %v", err)
	}
	if summary.Summary != "K\n\n以下是用户原话：\n1. B" {
		t.Fatalf("summary = %q", summary.Summary)
	}
	messages, err := store.Messages().ListBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var foundRawJ bool
	for _, message := range messages {
		if message.Role == storage.RoleUser && message.Content == "J" {
			foundRawJ = true
		}
	}
	if !foundRawJ {
		t.Fatalf("raw user input J was not preserved: %#v", messages)
	}

	a.usageMu.Lock()
	a.pendingCompact[session.ID] = true
	a.usageMu.Unlock()
	if err := a.HandleMessage(ctx, "M"); err != nil {
		t.Fatalf("message after repeated compact: %v", err)
	}
	requests = f.chatRequests()
	if len(requests) != 5 {
		t.Fatalf("requests after repeated compact = %d, want 5", len(requests))
	}
	repeatedPayload := llm.SegmentsContentText(requests[3].Messages[1].Segments)
	for _, want := range []string{"user: K", "以下是用户原话：", "当前用户输入：\nJ", "用户原话：\n1. B\n2. J\n3. L"} {
		if !strings.Contains(repeatedPayload, want) {
			t.Fatalf("repeated compact payload missing %q:\n%s", want, repeatedPayload)
		}
	}
	if strings.Contains(repeatedPayload, "已有摘要") || strings.Contains(repeatedPayload, "已有较早摘要") {
		t.Fatalf("repeated compact used a special previous-summary field:\n%s", repeatedPayload)
	}
	repeatedSummary, err := store.ContextSummaries().LatestBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("load repeated summary: %v", err)
	}
	if repeatedSummary.Summary != "K2\n\n以下是用户原话：\n1. B\n2. J\n3. L" {
		t.Fatalf("repeated summary = %q", repeatedSummary.Summary)
	}
	mainAfterRepeated := llm.SegmentsContentText(requests[4].Messages[1].Segments)
	if !strings.Contains(mainAfterRepeated, repeatedSummary.Summary) || !strings.Contains(mainAfterRepeated, "当前用户输入：\nM") {
		t.Fatalf("main user after repeated compact = %q", mainAfterRepeated)
	}
}

func TestCompactBlocksSessionChangesAndStopCancels(t *testing.T) {
	ctx := context.Background()
	p := &fakePlatform{}
	started := make(chan struct{})
	f := &fakeLLM{replies: []string{"K"}, chatBlocks: []fakeLLMBlock{{started: started, release: make(chan struct{})}}}
	store := newTestStore(t)
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	session, err := a.sessions.Create(ctx, a.scope(ctx), "compact")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, message := range []*storage.Message{
		{SessionID: session.ID, Role: storage.RoleUser, Content: "B"},
		{SessionID: session.ID, Role: storage.RoleAssistant, Content: "H"},
	} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	a.usageMu.Lock()
	a.pendingCompact[session.ID] = true
	a.usageMu.Unlock()

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
	if err := a.HandleMessage(ctx, "/delete "+session.ID+" --confirm"); err != nil {
		t.Fatalf("blocked /delete: %v", err)
	}
	current, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil || current.ID != session.ID {
		t.Fatalf("current session = %#v, err = %v", current, err)
	}
	if _, err := store.Sessions().Get(ctx, session.ID); err != nil {
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
	if got := a.turns.Snapshot(session.ID).Phase; got != turn.PhaseIdle {
		t.Fatalf("turn phase = %s", got)
	}
	if got := len(a.requests.ListBySession(session.ID)); got != 0 {
		t.Fatalf("active requests = %d", got)
	}
	if _, err := store.ContextSummaries().LatestBySession(ctx, session.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("summary after cancel err = %v", err)
	}
	if !a.shouldCompact(session.ID) {
		t.Fatal("pending compact flag was cleared after cancellation")
	}
	if got := p.out.String(); !strings.Contains(got, "暂不执行 /new") || !strings.Contains(got, "暂不执行 /delete") || !strings.Contains(got, "stopped 1 request") {
		t.Fatalf("output = %q", got)
	}
}

func TestCompactBlockedCommandSet(t *testing.T) {
	for _, name := range []string{"new", "resume", "fork", "work", "chat", "archive", "unarchive", "pin", "unpin", "rename", "delete", "clean", "compact"} {
		if !shouldBlockCommandDuringCompact(name) {
			t.Fatalf("command %q should be blocked", name)
		}
	}
	for _, name := range []string{"stop", "stopall", "requests", "status", "sessions", "messages"} {
		if shouldBlockCommandDuringCompact(name) {
			t.Fatalf("command %q should remain available", name)
		}
	}
}
