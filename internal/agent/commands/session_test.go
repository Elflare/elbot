package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/turn"
)

func TestFormatTimeForDisplay(t *testing.T) {
	tm := time.Date(2026, 5, 31, 14, 24, 45, 704514100, time.FixedZone("CST", 8*60*60))
	if got := formatTime(tm); got != "2026-05-31 14:24:45" {
		t.Fatalf("formatTime = %q", got)
	}
}

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

func TestLifecycleCommandsArchiveAndArchives(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

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
	last := []string{first.ID}
	deps := Deps{
		Sessions: svc,
		Requests: request.NewManager(0),
		Turns:    turn.NewManager(),
		Store:    store,
		Scope:    func(context.Context) session.Scope { return scope },
		SetLastSessions: func(summaries []storage.SessionSummary) {
			last = last[:0]
			for _, summary := range summaries {
				last = append(last, summary.ID)
			}
		},
		LastSessions:         func() []string { return last },
		SessionListPageSize:  func() int { return 1 },
		CleanupRetentionDays: func() int { return 30 },
	}

	preview, err := NewArchive(deps).Handle(ctx, command.Request{Args: "1"})
	if err != nil {
		t.Fatalf("archive preview: %v", err)
	}
	if !strings.Contains(preview.Content, "/archive 1 --confirm") || !strings.Contains(preview.Content, "title: first") || !strings.Contains(preview.Content, "id: "+first.ID) {
		t.Fatalf("archive preview = %q", preview.Content)
	}
	if _, err := NewArchive(deps).Handle(ctx, command.Request{Args: "1 --confirm"}); err != nil {
		t.Fatalf("archive confirm: %v", err)
	}

	list, err := NewArchives(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("archives list: %v", err)
	}
	if !strings.Contains(list.Content, "first") || strings.Contains(list.Content, "second") {
		t.Fatalf("archives list = %q", list.Content)
	}
	if !strings.Contains(list.Content, "page: 1") {
		t.Fatalf("archives list missing page: %q", list.Content)
	}

	deletePreview, err := NewDelete(deps).Handle(ctx, command.Request{Args: "1"})
	if err != nil {
		t.Fatalf("delete preview: %v", err)
	}
	if !strings.Contains(deletePreview.Content, "title: first") || !strings.Contains(deletePreview.Content, "id: "+first.ID) {
		t.Fatalf("delete preview = %q", deletePreview.Content)
	}

	if _, err := NewUnarchive(deps).Handle(ctx, command.Request{Args: "1"}); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	active, err := svc.List(ctx, scope, "first", 10)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 || active[0].ID != first.ID {
		t.Fatalf("active after unarchive = %#v", active)
	}
	_ = second
}

func TestResumeCommandCompletesSessionIDs(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

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
	cmd := NewResume(Deps{Sessions: svc, Scope: func(context.Context) session.Scope { return scope }, SessionListPageSize: func() int { return 10 }}).(command.Completer)

	got := cmd.Complete(ctx, command.CompletionRequest{Raw: "/resume ", Prefix: "/", Name: "resume", Args: "", Cursor: len("/resume ")})
	if len(got) != 2 {
		t.Fatalf("Complete empty = %#v", got)
	}
	prefix := first.ID[:8]
	got = cmd.Complete(ctx, command.CompletionRequest{Raw: "/resume " + prefix, Prefix: "/", Name: "resume", Args: prefix, Cursor: len("/resume ") + len(prefix)})
	if len(got) != 1 || got[0].Text != first.ID || got[0].Kind != "session_id" || got[0].Description != "first" {
		t.Fatalf("Complete prefix = %#v, first=%s second=%s", got, first.ID, second.ID)
	}
}

func TestSessionTargetCommandsCompleteSessionIDsAndConfirm(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc := session.NewService(store)
	scope := session.Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	first, err := svc.Create(ctx, scope, session.CreateRequest{Title: "first"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	archived, err := svc.Create(ctx, scope, session.CreateRequest{Title: "archived"})
	if err != nil {
		t.Fatalf("create archived: %v", err)
	}
	if _, err := svc.Archive(ctx, scope, archived.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	deps := Deps{Sessions: svc, Scope: func(context.Context) session.Scope { return scope }, SessionListPageSize: func() int { return 10 }}

	deleteCmd := NewDelete(deps).(command.Completer)
	prefix := first.ID[:8]
	got := deleteCmd.Complete(ctx, command.CompletionRequest{Raw: "/delete " + prefix, Prefix: "/", Name: "delete", Args: prefix, Cursor: len("/delete ") + len(prefix)})
	if len(got) != 1 || got[0].Text != first.ID || got[0].Kind != "session_id" {
		t.Fatalf("delete session Complete = %#v", got)
	}
	got = deleteCmd.Complete(ctx, command.CompletionRequest{Raw: "/delete " + first.ID + " --", Prefix: "/", Name: "delete", Args: first.ID + " --", Cursor: len("/delete ") + len(first.ID) + len(" --")})
	if len(got) != 1 || got[0].Text != "--confirm" {
		t.Fatalf("delete confirm Complete = %#v", got)
	}

	unarchiveCmd := NewUnarchive(deps).(command.Completer)
	got = unarchiveCmd.Complete(ctx, command.CompletionRequest{Raw: "/unarchive " + archived.ID[:8], Prefix: "/", Name: "unarchive", Args: archived.ID[:8], Cursor: len("/unarchive ") + 8})
	if len(got) != 1 || got[0].Text != archived.ID {
		t.Fatalf("unarchive Complete = %#v", got)
	}

	renameCmd := NewRename(deps).(command.Completer)
	got = renameCmd.Complete(ctx, command.CompletionRequest{Raw: "/rename " + prefix, Prefix: "/", Name: "rename", Args: prefix, Cursor: len("/rename ") + len(prefix)})
	if len(got) != 1 || got[0].Text != first.ID {
		t.Fatalf("rename target Complete = %#v", got)
	}
	got = renameCmd.Complete(ctx, command.CompletionRequest{Raw: "/rename " + first.ID + " new", Prefix: "/", Name: "rename", Args: first.ID + " new", Cursor: len("/rename ") + len(first.ID) + len(" new")})
	if len(got) != 0 {
		t.Fatalf("rename title should not complete = %#v", got)
	}
}

func TestResumeCommandEmitsAudit(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc := session.NewService(store)
	scope := session.Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	created, err := svc.Create(ctx, scope, session.CreateRequest{Title: "audited"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	var events []string
	lastSessions := []string{created.ID}
	deps := Deps{
		Sessions: svc,
		Store:    store,
		Scope:    func(context.Context) session.Scope { return scope },
		LastSessions: func() []string {
			return lastSessions
		},
		Audit: func(event string, attrs ...any) {
			events = append(events, event)
		},
	}

	if _, err := NewResume(deps).Handle(ctx, command.Request{Args: "1"}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(events) != 1 || events[0] != "session_resume" {
		t.Fatalf("events = %#v", events)
	}
}

func TestSessionsCommandCLIListsAllPlatformOwners(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc := session.NewService(store)
	qqScope := session.Scope{ActorID: "qqonebot:42", Platform: "qqonebot", PlatformScopeID: "group:9"}
	qqSession, err := svc.Create(ctx, qqScope, session.CreateRequest{Title: "qq visible"})
	if err != nil {
		t.Fatalf("create qq session: %v", err)
	}
	cliScope := session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	deps := Deps{
		Sessions:            svc,
		Scope:               func(context.Context) session.Scope { return cliScope },
		SetLastSessions:     func([]storage.SessionSummary) {},
		SessionListPageSize: func() int { return 20 },
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

func TestFormatSessionsShowsLifecycleMarkers(t *testing.T) {
	now := time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC)
	content := formatSessions([]storage.SessionSummary{{ID: "s1", Title: "one", UpdatedAt: now, PinnedAt: &now, ArchivedAt: &now}}, "s1")
	if !strings.Contains(content, "[current, pinned, archived]") {
		t.Fatalf("content = %q", content)
	}
}

func TestFormatSessionsPageShowsNextCommand(t *testing.T) {
	content := formatSessionsPage([]storage.SessionSummary{{ID: "s1", Title: "one", UpdatedAt: time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC)}}, "", 1, "foo", true, "/sessions")
	if !strings.Contains(content, "page: 1") || !strings.Contains(content, "next: /sessions 2 foo") {
		t.Fatalf("content = %q", content)
	}
	resume := formatSessionsPage([]storage.SessionSummary{{ID: "s1", Title: "one", UpdatedAt: time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC)}}, "", 2, "", false, "/resume --page")
	if !strings.Contains(resume, "prev: /resume --page 1") {
		t.Fatalf("resume content = %q", resume)
	}
}

func TestForkMessagesUseForkContextAndForkShowsHistory(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

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
