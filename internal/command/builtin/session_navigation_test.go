package builtin

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/testutil"
	"strings"
	"testing"
	"time"

	command "elbot/internal/command/share"
	globalevents "elbot/internal/events"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func TestFormatMessagePageIncludesConversationMessages(t *testing.T) {
	messages := conversationMessages([]storage.Message{
		{ID: "s1", Role: storage.RoleSystem, Content: "system prompt"},
		{ID: "u1", Role: storage.RoleUser, Content: "question"},
		{ID: "a1", Role: storage.RoleAssistant, Content: "answer one"},
		{ID: "t1", Role: storage.RoleTool, Content: "tool output"},
		{ID: "empty-user", Role: storage.RoleUser, Content: " \n\t"},
		{ID: "empty-bot", Role: storage.RoleAssistant},
		{ID: "u2", Role: storage.RoleUser, Content: "follow up"},
		{ID: "a2", Role: storage.RoleAssistant, Content: "answer two"},
	})
	for _, tc := range []struct {
		page int
		want string
	}{
		{page: 1, want: "page=1/2\nuser: question\nbot: [a1] answer one"},
		{page: 2, want: "page=2/2\nuser: follow up\nbot: [a2] answer two"},
	} {
		content, err := formatMessagePage(messages, tc.page, 2)
		if err != nil {
			t.Fatalf("formatMessagePage(%d): %v", tc.page, err)
		}
		if content != tc.want {
			t.Fatalf("page %d = %q, want %q", tc.page, content, tc.want)
		}
	}
	for _, page := range []int{0, 3} {
		if _, err := formatMessagePage(messages, page, 2); err == nil {
			t.Fatalf("expected out-of-range error for page %d", page)
		}
	}
}

func TestMessagesTruncateOnlyWhenFormatting(t *testing.T) {
	text := " \t" + strings.Repeat("中😀", 21) + "\n尾 "
	source := []storage.Message{
		{ID: "u1", Role: storage.RoleUser, Content: text},
		{ID: "a1", Role: storage.RoleAssistant, Content: text},
	}
	messages := conversationMessages(source)
	if len(messages) != len(source) {
		t.Fatalf("messages = %v", messages)
	}
	for _, message := range messages {
		if message.Content != text {
			t.Fatalf("filter changed content: %q", message.Content)
		}
	}
	content, err := formatMessagePage(messages, 1, messageListPageSize)
	if err != nil {
		t.Fatalf("formatMessagePage: %v", err)
	}
	preview := strings.Repeat("中😀", 20) + "..."
	want := "page=1/1\nuser: " + preview + "\nbot: [a1] " + preview
	if content != want {
		t.Fatalf("content = %q, want %q", content, want)
	}
	for i := range source {
		if source[i].Content != text || messages[i].Content != text {
			t.Fatalf("formatting changed message %d", i)
		}
	}
}

func TestMessagesHandlesEmptyAndUserOnlySessions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []storage.Message
		want     string
	}{
		{name: "empty", want: "nil"},
		{
			name: "blank messages",
			messages: []storage.Message{
				{Role: storage.RoleUser, Content: " \n\t"},
				{Role: storage.RoleAssistant, Content: " "},
			},
			want: "nil",
		},
		{
			name:     "user only",
			messages: []storage.Message{{Role: storage.RoleUser, Content: "question"}},
			want:     "page=1/1\nuser: question",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := newCommandTestStore(t)
			svc := newSessionSupervisor(t, store)
			scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
			row, err := svc.Create(ctx, scope, session.CreateRequest{})
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			for _, message := range tc.messages {
				message.ID = storage.NewID()
				message.SessionID = row.ID
				if err := store.Messages().Append(ctx, &message); err != nil {
					t.Fatalf("append message: %v", err)
				}
			}
			deps := Deps{
				CommandText: testCommandText,
				Sessions:    svc,
				Store:       store,
				Scope:       func(context.Context) session.Scope { return scope },
			}
			result, err := NewMessages(deps).Handle(ctx, command.Request{})
			if err != nil {
				t.Fatalf("messages handle: %v", err)
			}
			if result.Text != tc.want {
				t.Fatalf("content = %q, want %q", result.Text, tc.want)
			}
		})
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

func TestPreviewContentTruncatesLongMessages(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{name: "empty"},
		{name: "short", text: "short message", want: "short message"},
		{name: "whitespace", text: "  first\nsecond  ", want: "first second"},
		{name: "at limit", text: strings.Repeat("a", 80), want: strings.Repeat("a", 80)},
		{name: "over limit", text: strings.Repeat("a", 81), want: strings.Repeat("a", 80) + "..."},
		{name: "unicode at limit", text: strings.Repeat("中😀", 40), want: strings.Repeat("中😀", 40)},
		{name: "unicode over limit", text: strings.Repeat("中😀", 41), want: strings.Repeat("中😀", 40) + "..."},
		{name: "trim before limit", text: "  " + strings.Repeat("中", 80) + "\n ", want: strings.Repeat("中", 80)},
		{name: "newline before limit", text: strings.Repeat("中", 79) + "\n尾", want: strings.Repeat("中", 79) + " ..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := previewContent(tc.text); got != tc.want {
				t.Fatalf("previewContent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResumeAndForkTruncateHistoryWithoutChangingMessages(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)
	svc := newSessionSupervisor(t, store)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
	parent, err := svc.Create(ctx, scope, session.CreateRequest{Title: "parent"})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	original := strings.Repeat("中😀", 101)
	messages := []*storage.Message{
		{ID: storage.NewID(), SessionID: parent.ID, Role: storage.RoleUser, Content: original},
		{ID: storage.NewID(), SessionID: parent.ID, Role: storage.RoleAssistant, Content: original},
	}
	for _, message := range messages {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	if _, err := svc.Create(ctx, scope, session.CreateRequest{Title: "current"}); err != nil {
		t.Fatalf("create current: %v", err)
	}
	deps := Deps{CommandText: testCommandText,
		Sessions: svc,
		Requests: request.NewManager(testutil.IDSequence()),
		Turns:    turn.NewSupervisor(testutil.IDSequence()),
		Store:    store,
		Scope:    func(context.Context) session.Scope { return scope },
	}
	wantHistory := "user: " + strings.Repeat("中😀", 40) + "...\nbot: " + strings.Repeat("中😀", 40) + "..."
	for _, tc := range []struct {
		name    string
		handler command.Handler
		args    string
	}{
		{name: "resume", handler: NewResume(deps), args: parent.ID},
		{name: "fork", handler: NewFork(deps), args: messages[1].ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.handler.Handle(ctx, command.Request{Args: tc.args})
			if err != nil {
				t.Fatalf("handle: %v", err)
			}
			if !strings.HasSuffix(result.Text, wantHistory) {
				t.Fatalf("unexpected history: %q", result.Text)
			}
			for _, message := range messages {
				stored, err := store.Messages().Get(ctx, message.ID)
				if err != nil {
					t.Fatalf("get message: %v", err)
				}
				if stored.Content != original || stored.Role != message.Role {
					t.Fatalf("stored message changed: %+v", stored)
				}
			}
		})
	}
}

func TestResumeCommandCompletesSessionIDs(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)

	svc := newSessionSupervisor(t, store)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
	first, err := svc.Create(ctx, scope, session.CreateRequest{Title: "first"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := svc.Create(ctx, scope, session.CreateRequest{Title: "second"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	cmd := NewResume(Deps{CommandText: testCommandText, Sessions: svc, Scope: func(context.Context) session.Scope { return scope }, SessionState: NewSessionCommandState(10, 30)}).(command.Completer)

	got := cmd.Complete(ctx, command.CompletionRequest{Raw: "/resume ", Prefix: "/", Name: "resume", Args: "", Cursor: len("/resume ")})
	if len(got) != 1 || got[0].Text != first.ID {
		t.Fatalf("Complete empty = %#v", got)
	}
	prefix := first.ID[:len(first.ID)-1]
	got = cmd.Complete(ctx, command.CompletionRequest{Raw: "/resume " + prefix, Prefix: "/", Name: "resume", Args: prefix, Cursor: len("/resume ") + len(prefix)})
	if len(got) != 1 || got[0].Text != first.ID || got[0].Kind != "session_id" || got[0].Description != "first" {
		t.Fatalf("Complete prefix = %#v, first=%s second=%s", got, first.ID, second.ID)
	}
}

func TestResumeCommandEmitsAudit(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)

	svc := newSessionSupervisor(t, store)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
	created, err := svc.Create(ctx, scope, session.CreateRequest{Title: "audited"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.Create(ctx, scope, session.CreateRequest{Title: "current"}); err != nil {
		t.Fatalf("create current session: %v", err)
	}
	var events []string
	connection, _ := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) {
		if record.Category == globalevents.LogAudit {
			events = append(events, record.EventName)
		}

	}, signal.ConnectOptions{})
	defer connection.Disconnect()
	selections := NewSessionCommandState(10, 30)
	deps := Deps{CommandText: testCommandText,
		Sessions:     svc,
		Store:        store,
		Scope:        func(context.Context) session.Scope { return scope },
		SessionState: selections,
	}

	result, err := NewResume(deps).Handle(ctx, command.Request{Args: "1"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !strings.Contains(result.Text, created.ID) {
		t.Fatalf("resume result = %q", result.Text)
	}
	if len(events) != 1 || events[0] != "session_resume" {
		t.Fatalf("events = %#v", events)
	}
}

func TestResumeCommandUsesPinnedNonCurrentGlobalIndex(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)
	svc := newSessionSupervisor(t, store)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
	now := storage.Now()

	older, err := svc.Create(ctx, scope, session.CreateRequest{Title: "older pinned"})
	if err != nil {
		t.Fatalf("create older: %v", err)
	}
	older.UpdatedAt = now.Add(-2 * time.Hour)
	older.PinnedAt = &now
	if _, err := store.Sessions().Mutate(ctx, older.ID, func(latest *storage.Session) error { *latest = *older; return nil }); err != nil {
		t.Fatalf("update older: %v", err)
	}
	recent, err := svc.Create(ctx, scope, session.CreateRequest{Title: "recent"})
	if err != nil {
		t.Fatalf("create recent: %v", err)
	}
	recent.UpdatedAt = now.Add(-time.Hour)
	if _, err := store.Sessions().Mutate(ctx, recent.ID, func(latest *storage.Session) error { *latest = *recent; return nil }); err != nil {
		t.Fatalf("update recent: %v", err)
	}
	current, err := svc.Create(ctx, scope, session.CreateRequest{Title: "current"})
	if err != nil {
		t.Fatalf("create current: %v", err)
	}

	deps := Deps{CommandText: testCommandText, Sessions: svc, Store: store, Scope: func(context.Context) session.Scope { return scope }, SessionState: NewSessionCommandState(1, 30)}
	page, err := NewResume(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("resume page: %v", err)
	}
	if !strings.Contains(page.Text, "[1] older pinned") || strings.Contains(page.Text, current.ID) {
		t.Fatalf("resume page = %q", page.Text)
	}
	secondPage, err := NewResume(deps).Handle(ctx, command.Request{Args: "--page 2"})
	if err != nil {
		t.Fatalf("resume page 2: %v", err)
	}
	if !strings.Contains(secondPage.Text, "[2] recent") {
		t.Fatalf("resume page 2 = %q", secondPage.Text)
	}
	listed, err := NewSessions(deps).Handle(ctx, command.Request{})
	if err != nil || !strings.Contains(listed.Text, "[1] older pinned") {
		t.Fatalf("sessions disagree with resume: %+v, %v", listed, err)
	}
	completions := NewResume(deps).(command.Completer).Complete(ctx, command.CompletionRequest{Raw: "/resume ", Prefix: "/", Name: "resume", Cursor: len("/resume ")})
	if len(completions) != 1 || completions[0].Text != older.ID {
		t.Fatalf("resume completions = %+v", completions)
	}

	// 使用没有列表缓存的新命令状态，直接编号恢复仍遵循统一排序。
	deps.SessionState = NewSessionCommandState(1, 30)
	result, err := NewResume(deps).Handle(ctx, command.Request{Args: "1"})
	if err != nil {
		t.Fatalf("resume 1: %v", err)
	}
	if !strings.Contains(result.Text, older.ID) {
		t.Fatalf("resume result = %q", result.Text)
	}
	resumed, err := svc.Current(ctx, scope)
	if err != nil || resumed.ID != older.ID {
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

	svc := newSessionSupervisor(t, store)
	qqScope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "qqonebot", ID: "42"}, Platform: "qqonebot", ChatType: "group", ChatID: "9"}
	qqSession, err := svc.Create(ctx, qqScope, session.CreateRequest{Title: "qq visible"})
	if err != nil {
		t.Fatalf("create qq session: %v", err)
	}
	cliScope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
	deps := Deps{CommandText: testCommandText,
		Sessions:     svc,
		Scope:        func(context.Context) session.Scope { return cliScope },
		SessionState: NewSessionCommandState(20, 30),
	}

	result, err := NewSessions(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("sessions handle: %v", err)
	}
	if !strings.Contains(result.Text, qqSession.ID) || !strings.Contains(result.Text, "qq visible") {
		t.Fatalf("sessions result missing qq session: %q", result.Text)
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

	svc := newSessionSupervisor(t, store)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
	parent, err := svc.Create(ctx, scope, session.CreateRequest{Title: "parent"})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	user := &storage.Message{ID: storage.NewID(), SessionID: parent.ID, Role: storage.RoleUser, Content: "你好"}
	assistant := &storage.Message{ID: storage.NewID(), SessionID: parent.ID, Role: storage.RoleAssistant, Content: "这是可以 fork 的回复"}
	ignored := &storage.Message{ID: storage.NewID(), SessionID: parent.ID, Role: storage.RoleAssistant, Content: "fork 点之后不该出现"}
	for _, message := range []*storage.Message{user, assistant, ignored} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	deps := Deps{CommandText: testCommandText,
		Sessions: svc,
		Requests: request.NewManager(testutil.IDSequence()),
		Turns:    turn.NewSupervisor(testutil.IDSequence()),
		Store:    store,
		Scope:    func(context.Context) session.Scope { return scope },
	}
	forkResult, err := NewFork(deps).Handle(ctx, command.Request{Args: assistant.ID})
	if err != nil {
		t.Fatalf("fork handle: %v", err)
	}
	if !strings.Contains(forkResult.Text, "bot:") || !strings.Contains(forkResult.Text, "这是可以 fork 的回复") {
		t.Fatalf("fork result = %q", forkResult.Text)
	}

	messagesResult, err := NewMessages(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("messages handle: %v", err)
	}
	want := "page=1/1\nuser: 你好\nbot: [" + assistant.ID + "] 这是可以 fork 的回复"
	if messagesResult.Text != want {
		t.Fatalf("messages result = %q, want %q", messagesResult.Text, want)
	}
	if strings.Contains(messagesResult.Text, ignored.ID) || strings.Contains(messagesResult.Text, "fork 点之后不该出现") {
		t.Fatalf("messages result included post-fork message: %q", messagesResult.Text)
	}
}
