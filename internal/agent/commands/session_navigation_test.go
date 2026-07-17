package commands

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
	"strings"
	"testing"
	"time"
)

func TestFormatMessagePageOnlyUsesAssistantMessages(t *testing.T) {
	messages := assistantMessages([]storage.Message{
		{ID: "u1", Role: storage.RoleUser, Content: "question"},
		{ID: "a1", Role: storage.RoleAssistant, Content: "answer one"},
		{ID: "a2", Role: storage.RoleAssistant, Content: "answer two"},
	})
	content, err := formatMessagePage(messages, 1, 1)
	if err != nil {
		t.Fatalf("formatMessagePage: %v", err)
	}
	if !strings.Contains(content, "messages page 1/2") || !strings.Contains(content, "a1: answer one") {
		t.Fatalf("content = %q", content)
	}
	if strings.Contains(content, "u1") || strings.Contains(content, "assistant") {
		t.Fatalf("content should not include user messages or role labels: %q", content)
	}
}

func TestMessagePreviewTruncatesAndFlattensWhitespace(t *testing.T) {
	got := messagePreview("这是一个很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长的回答\n第二行")
	if strings.Contains(got, "\n") {
		t.Fatalf("preview contains newline: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("preview should be truncated: %q", got)
	}
}

func TestResumeCommandCompletesSessionIDs(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)

	svc := session.NewService(store)
	scope := session.Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	first, err := svc.Create(ctx, scope, session.CreateRequest{Title: "first"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := svc.Create(ctx, scope, session.CreateRequest{Title: "second"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	cmd := NewResume(Deps{Sessions: svc, Scope: func(context.Context) session.Scope { return scope }, SessionState: NewSessionCommandState(10, 30)}).(command.Completer)

	got := cmd.Complete(ctx, command.CompletionRequest{Raw: "/resume ", Prefix: "/", Name: "resume", Args: "", Cursor: len("/resume ")})
	if len(got) != 1 || got[0].Text != first.ID {
		t.Fatalf("Complete empty = %#v", got)
	}
	prefix := first.ID[:8]
	got = cmd.Complete(ctx, command.CompletionRequest{Raw: "/resume " + prefix, Prefix: "/", Name: "resume", Args: prefix, Cursor: len("/resume ") + len(prefix)})
	if len(got) != 1 || got[0].Text != first.ID || got[0].Kind != "session_id" || got[0].Description != "first" {
		t.Fatalf("Complete prefix = %#v, first=%s second=%s", got, first.ID, second.ID)
	}
}

func TestResumeCommandEmitsAudit(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)

	svc := session.NewService(store)
	scope := session.Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	created, err := svc.Create(ctx, scope, session.CreateRequest{Title: "audited"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.Create(ctx, scope, session.CreateRequest{Title: "current"}); err != nil {
		t.Fatalf("create current session: %v", err)
	}
	var events []string
	selections := NewSessionCommandState(10, 30)
	deps := Deps{
		Sessions:     svc,
		Store:        store,
		Scope:        func(context.Context) session.Scope { return scope },
		SessionState: selections,
		Audit: func(event string, attrs ...any) {
			events = append(events, event)
		},
	}

	result, err := NewResume(deps).Handle(ctx, command.Request{Args: "1"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !strings.Contains(result.Content, created.ID) {
		t.Fatalf("resume result = %q", result.Content)
	}
	if len(events) != 1 || events[0] != "session_resume" {
		t.Fatalf("events = %#v", events)
	}
}

func TestResumeCommandUsesRecentNonCurrentGlobalIndex(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)
	svc := session.NewService(store)
	scope := session.Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	now := storage.Now()

	older, err := svc.Create(ctx, scope, session.CreateRequest{Title: "older pinned"})
	if err != nil {
		t.Fatalf("create older: %v", err)
	}
	older.UpdatedAt = now.Add(-2 * time.Hour)
	older.PinnedAt = &now
	if err := store.Sessions().Update(ctx, older); err != nil {
		t.Fatalf("update older: %v", err)
	}
	recent, err := svc.Create(ctx, scope, session.CreateRequest{Title: "recent"})
	if err != nil {
		t.Fatalf("create recent: %v", err)
	}
	recent.UpdatedAt = now.Add(-time.Hour)
	if err := store.Sessions().Update(ctx, recent); err != nil {
		t.Fatalf("update recent: %v", err)
	}
	current, err := svc.Create(ctx, scope, session.CreateRequest{Title: "current"})
	if err != nil {
		t.Fatalf("create current: %v", err)
	}

	deps := Deps{Sessions: svc, Store: store, Scope: func(context.Context) session.Scope { return scope }, SessionState: NewSessionCommandState(1, 30)}
	page, err := NewResume(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("resume page: %v", err)
	}
	if !strings.Contains(page.Content, "[1] recent") || strings.Contains(page.Content, current.ID) {
		t.Fatalf("resume page = %q", page.Content)
	}
	secondPage, err := NewResume(deps).Handle(ctx, command.Request{Args: "--page 2"})
	if err != nil {
		t.Fatalf("resume page 2: %v", err)
	}
	if !strings.Contains(secondPage.Content, "[2] older pinned") {
		t.Fatalf("resume page 2 = %q", secondPage.Content)
	}

	result, err := NewResume(deps).Handle(ctx, command.Request{Args: "1"})
	if err != nil {
		t.Fatalf("resume 1: %v", err)
	}
	if !strings.Contains(result.Content, recent.ID) {
		t.Fatalf("resume result = %q", result.Content)
	}
	resumed, err := svc.Current(ctx, scope)
	if err != nil || resumed.ID != recent.ID {
		t.Fatalf("current = %#v, err=%v", resumed, err)
	}
	if _, err := NewResume(deps).Handle(ctx, command.Request{Args: "0"}); err == nil {
		t.Fatal("resume 0 should fail")
	}
	if _, err := NewResume(deps).Handle(ctx, command.Request{Args: "3"}); err == nil {
		t.Fatal("resume out of range should fail")
	}
}

func TestSessionsCommandCLIListsAllPlatformOwners(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)

	svc := session.NewService(store)
	qqScope := session.Scope{ActorID: "qqonebot:42", Platform: "qqonebot", PlatformScopeID: "group:9"}
	qqSession, err := svc.Create(ctx, qqScope, session.CreateRequest{Title: "qq visible"})
	if err != nil {
		t.Fatalf("create qq session: %v", err)
	}
	cliScope := session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	deps := Deps{
		Sessions:     svc,
		Scope:        func(context.Context) session.Scope { return cliScope },
		SessionState: NewSessionCommandState(20, 30),
	}

	result, err := NewSessions(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("sessions handle: %v", err)
	}
	if !strings.Contains(result.Content, qqSession.ID) || !strings.Contains(result.Content, "qq visible") {
		t.Fatalf("sessions result missing qq session: %q", result.Content)
	}
}

func TestParseSessionsArgs(t *testing.T) {
	for _, tc := range []struct {
		args      string
		wantPage  int
		wantQuery string
	}{
		{"", 1, ""},
		{"2", 2, ""},
		{"foo", 1, "foo"},
		{"2 foo bar", 2, "foo bar"},
	} {
		page, query, err := parseSessionsArgs(tc.args)
		if err != nil {
			t.Fatalf("parseSessionsArgs(%q): %v", tc.args, err)
		}
		if page != tc.wantPage || query != tc.wantQuery {
			t.Fatalf("parseSessionsArgs(%q) = %d/%q, want %d/%q", tc.args, page, query, tc.wantPage, tc.wantQuery)
		}
	}
}

func TestParseResumePageArg(t *testing.T) {
	page, err := parseResumePageArg("--page 2")
	if err != nil {
		t.Fatalf("parseResumePageArg: %v", err)
	}
	if page != 2 {
		t.Fatalf("page = %d", page)
	}
	if _, err := parseResumePageArg("2"); err == nil {
		t.Fatal("expected usage error")
	}
}

func TestForkMessagesUseForkContextAndForkShowsHistory(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)

	svc := session.NewService(store)
	scope := session.Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	parent, err := svc.Create(ctx, scope, session.CreateRequest{Title: "parent"})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	user := &storage.Message{SessionID: parent.ID, Role: storage.RoleUser, Content: "你好"}
	assistant := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "这是可以 fork 的回复"}
	ignored := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "fork 点之后不该出现"}
	for _, message := range []*storage.Message{user, assistant, ignored} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	deps := Deps{
		Sessions: svc,
		Requests: request.NewManager(0),
		Turns:    turn.NewManager(),
		Store:    store,
		Scope:    func(context.Context) session.Scope { return scope },
	}
	forkResult, err := NewFork(deps).Handle(ctx, command.Request{Args: assistant.ID})
	if err != nil {
		t.Fatalf("fork handle: %v", err)
	}
	if !strings.Contains(forkResult.Content, "recent messages:") || !strings.Contains(forkResult.Content, "这是可以 fork 的回复") {
		t.Fatalf("fork result = %q", forkResult.Content)
	}

	messagesResult, err := NewMessages(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("messages handle: %v", err)
	}
	if !strings.Contains(messagesResult.Content, assistant.ID) {
		t.Fatalf("messages result missing fork point: %q", messagesResult.Content)
	}
	if strings.Contains(messagesResult.Content, ignored.ID) || strings.Contains(messagesResult.Content, "fork 点之后不该出现") {
		t.Fatalf("messages result included post-fork message: %q", messagesResult.Content)
	}
}
