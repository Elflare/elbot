package builtin

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/testutil"
	"errors"
	"fmt"
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

func TestMessagesKeepFullTextAndReturnLatestPage(t *testing.T) {
	ctx := t.Context()
	store := newCommandTestStore(t)
	svc := newSessionSupervisor(t, store)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
	row, err := svc.Create(ctx, scope, session.CreateRequest{Title: "history"})
	if err != nil {
		t.Fatal(err)
	}
	text := "  " + strings.Repeat("中😀", 30) + "\n尾 "
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		role := storage.RoleUser
		if i%2 == 1 {
			role = storage.RoleAssistant
		}
		m := storage.Message{ID: fmt.Sprintf("m-%d", i), SessionID: row.ID, Role: role, Content: text, CreatedAt: fixed}
		if err := store.Messages().Append(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewMessages(Deps{CommandText: testCommandText, Sessions: svc, Store: store, Scope: func(context.Context) session.Scope { return scope }})
	for _, tc := range []struct {
		args        string
		page, count int
		first, last string
	}{{"", 1, 20, "m-5", "m-24"}, {"2", 2, 5, "m-0", "m-4"}} {
		result, err := handler.Handle(ctx, command.Request{Args: tc.args})
		if err != nil {
			t.Fatal(err)
		}
		data := result.Data.(command.MessagesData)
		if result.Kind != command.ResultKindMessages || data.Page.Page != tc.page || *data.TotalPages != 2 || len(data.Items) != tc.count || data.Items[0].ID != tc.first || data.Items[len(data.Items)-1].ID != tc.last {
			t.Fatalf("page=%+v", data)
		}
		for _, m := range data.Items {
			if m.Content != text {
				t.Fatalf("body was truncated: %q", m.Content)
			}
		}
		if strings.Contains(commandResultText(result), text) {
			t.Fatal("text preview should remain compact")
		}
	}
	for _, args := range []string{"0", "3", "x"} {
		_, err := handler.Handle(ctx, command.Request{Args: args})
		var failure *command.Error
		if !errors.As(err, &failure) || failure.Kind != command.ErrorInput {
			t.Fatalf("args=%s err=%v", args, err)
		}
	}
}

func TestMessagesEmptyHistoryIsStructured(t *testing.T) {
	store := newCommandTestStore(t)
	svc := newSessionSupervisor(t, store)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u"}, Platform: "cli", ChatID: "local", ChatType: "private", IsCLI: true}
	row, err := svc.Create(t.Context(), scope, session.CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewMessages(Deps{CommandText: testCommandText, Sessions: svc, Store: store, Scope: func(context.Context) session.Scope { return scope }}).Handle(t.Context(), command.Request{})
	if err != nil {
		t.Fatal(err)
	}
	data := result.Data.(command.MessagesData)
	if data.SessionID != row.ID || data.Items == nil || len(data.Items) != 0 || *data.TotalPages != 0 {
		t.Fatalf("data=%+v", data)
	}
	m := storage.Message{ID: "attachment", SessionID: row.ID, Role: storage.RoleUser, Segments: `[{"type":"file","name":"note.txt"}]`}
	if err := store.Messages().Append(t.Context(), &m); err != nil {
		t.Fatal(err)
	}
	result, err = NewMessages(Deps{CommandText: testCommandText, Sessions: svc, Store: store, Scope: func(context.Context) session.Scope { return scope }}).Handle(t.Context(), command.Request{})
	if err != nil {
		t.Fatal(err)
	}
	data = result.Data.(command.MessagesData)
	if len(data.Items) != 1 || len(data.Items[0].Attachments) != 1 || data.Items[0].Attachments[0].Name != "note.txt" {
		t.Fatalf("data=%+v", data)
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
			if !strings.HasSuffix(commandResultText(result), wantHistory) {
				t.Fatalf("unexpected history: %q", commandResultText(result))
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
	if !strings.Contains(commandResultText(result), created.ID) {
		t.Fatalf("resume result = %q", commandResultText(result))
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
	if !strings.Contains(commandResultText(page), "[1] older pinned") || strings.Contains(commandResultText(page), current.ID) {
		t.Fatalf("resume page = %q", commandResultText(page))
	}
	secondPage, err := NewResume(deps).Handle(ctx, command.Request{Args: "--page 2"})
	if err != nil {
		t.Fatalf("resume page 2: %v", err)
	}
	if !strings.Contains(commandResultText(secondPage), "[2] recent") {
		t.Fatalf("resume page 2 = %q", commandResultText(secondPage))
	}
	listed, err := NewSessions(deps).Handle(ctx, command.Request{})
	if err != nil || !strings.Contains(commandResultText(listed), "[1] older pinned") {
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
	if !strings.Contains(commandResultText(result), older.ID) {
		t.Fatalf("resume result = %q", commandResultText(result))
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
	if !strings.Contains(commandResultText(result), qqSession.ID) || !strings.Contains(commandResultText(result), "qq visible") {
		t.Fatalf("sessions result missing qq session: %q", commandResultText(result))
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
	if !strings.Contains(commandResultText(forkResult), "bot:") || !strings.Contains(commandResultText(forkResult), "这是可以 fork 的回复") {
		t.Fatalf("fork result = %q", commandResultText(forkResult))
	}

	messagesResult, err := NewMessages(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("messages handle: %v", err)
	}
	want := "page=1/1\nuser: 你好\nbot: [" + assistant.ID + "] 这是可以 fork 的回复"
	if commandResultText(messagesResult) != want {
		t.Fatalf("messages result = %q, want %q", commandResultText(messagesResult), want)
	}
	if strings.Contains(commandResultText(messagesResult), ignored.ID) || strings.Contains(commandResultText(messagesResult), "fork 点之后不该出现") {
		t.Fatalf("messages result included post-fork message: %q", commandResultText(messagesResult))
	}
}
