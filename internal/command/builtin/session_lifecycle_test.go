package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/testutil"
	"elbot/internal/turn"
	"strings"
	"testing"
	"time"
)

func TestLifecycleCommandsArchiveAndArchives(t *testing.T) {
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
	deps := Deps{CommandText: testCommandText,
		Sessions:            svc,
		Requests:            request.NewManager(testutil.IDSequence()),
		Turns:               turn.NewSupervisor(testutil.IDSequence()),
		Store:               store,
		Scope:               func(context.Context) session.Scope { return scope },
		SessionListPageSize: 1,
	}

	preview, err := NewArchive(deps).Handle(ctx, command.Request{Args: first.ID})
	if err != nil {
		t.Fatalf("archive preview: %v", err)
	}
	if !strings.Contains(commandResultText(preview), "/archive "+first.ID+" --confirm") || !strings.Contains(commandResultText(preview), "archive? first") || !strings.Contains(commandResultText(preview), first.ID) {
		t.Fatalf("archive preview = %q", commandResultText(preview))
	}
	if _, err := NewArchive(deps).Handle(ctx, command.Request{Args: first.ID + " --confirm"}); err != nil {
		t.Fatalf("archive confirm: %v", err)
	}

	list, err := NewArchives(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("archives list: %v", err)
	}
	if !strings.Contains(commandResultText(list), "first") || strings.Contains(commandResultText(list), "second") {
		t.Fatalf("archives list = %q", commandResultText(list))
	}
	if !strings.Contains(commandResultText(list), "page=1") {
		t.Fatalf("archives list missing page: %q", commandResultText(list))
	}

	deletePreview, err := NewDelete(deps).Handle(ctx, command.Request{Args: first.ID})
	if err != nil {
		t.Fatalf("delete preview: %v", err)
	}
	if !strings.Contains(commandResultText(deletePreview), "delete? first") || !strings.Contains(commandResultText(deletePreview), first.ID) {
		t.Fatalf("delete preview = %q", commandResultText(deletePreview))
	}
	if !strings.Contains(commandResultText(deletePreview), "/delete "+first.ID+" --confirm") {
		t.Fatalf("delete preview does not pin canonical id: %q", commandResultText(deletePreview))
	}

	if _, err := NewUnarchive(deps).Handle(ctx, command.Request{Args: first.ID}); err != nil {
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

func TestArchiveCurrentConfirmationPinsSessionID(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)
	svc := newSessionSupervisor(t, store)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
	current, err := svc.Create(ctx, scope, session.CreateRequest{Title: "current"})
	if err != nil {
		t.Fatalf("create current: %v", err)
	}
	deps := Deps{CommandText: testCommandText,
		Sessions: svc,
		Store:    store,
		Scope:    func(context.Context) session.Scope { return scope },
	}

	preview, err := NewArchive(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("archive preview: %v", err)
	}
	if !strings.Contains(commandResultText(preview), "/archive "+current.ID+" --confirm") {
		t.Fatalf("archive preview does not pin current id: %q", commandResultText(preview))
	}

	replacement, err := svc.Create(ctx, scope, session.CreateRequest{Title: "replacement"})
	if err != nil {
		t.Fatalf("create replacement: %v", err)
	}
	if _, err := NewArchive(deps).Handle(ctx, command.Request{Args: current.ID + " --confirm"}); err != nil {
		t.Fatalf("archive pinned current: %v", err)
	}
	archived, err := svc.ListPage(ctx, scope, session.ListRequest{PageSize: 10, ArchivedOnly: true})
	if err != nil {
		t.Fatalf("list archived: %v", err)
	}
	if len(archived.Items) != 1 || archived.Items[0].ID != current.ID {
		t.Fatalf("archived sessions = %#v", archived)
	}
	active, err := svc.Current(ctx, scope)
	if err != nil {
		t.Fatalf("current after archive: %v", err)
	}
	if active.ID != replacement.ID {
		t.Fatalf("current = %s, want %s", active.ID, replacement.ID)
	}
}

func TestSessionTargetCommandsCompleteSessionIDsAndConfirm(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)

	svc := newSessionSupervisor(t, store)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
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
	deps := Deps{CommandText: testCommandText, Sessions: svc, Scope: func(context.Context) session.Scope { return scope }, SessionListPageSize: 10}

	deleteCmd := NewDelete(deps).(command.Completer)
	prefix := first.ID[:len(first.ID)-1]
	got := deleteCmd.Complete(ctx, command.CompletionRequest{Raw: "/delete " + prefix, Prefix: "/", Name: "delete", Args: prefix, Cursor: len("/delete ") + len(prefix)})
	if len(got) != 1 || got[0].Text != first.ID || got[0].Kind != "session_id" {
		t.Fatalf("delete session Complete = %#v", got)
	}
	got = deleteCmd.Complete(ctx, command.CompletionRequest{Raw: "/delete " + first.ID + " --", Prefix: "/", Name: "delete", Args: first.ID + " --", Cursor: len("/delete ") + len(first.ID) + len(" --")})
	if len(got) != 1 || got[0].Text != "--confirm" {
		t.Fatalf("delete confirm Complete = %#v", got)
	}

	unarchiveCmd := NewUnarchive(deps).(command.Completer)
	archivedPrefix := archived.ID[:len(archived.ID)-1]
	got = unarchiveCmd.Complete(ctx, command.CompletionRequest{Raw: "/unarchive " + archivedPrefix, Prefix: "/", Name: "unarchive", Args: archivedPrefix, Cursor: len("/unarchive ") + len(archivedPrefix)})
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

func TestFormatSessionsShowsLifecycleMarkers(t *testing.T) {
	now := time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC)
	item := command.SessionSummaryData{SessionData: command.SessionData{ID: "s1", Title: "one", UpdatedAt: now, PinnedAt: &now, ArchivedAt: &now}}
	content := commandResultText(&command.Result{Kind: command.ResultKindSessions, Data: command.SessionsData{Page: command.Page{Page: 1, PageSize: 10}, Items: []command.SessionSummaryData{item}, CurrentSessionID: "s1"}})
	if !strings.Contains(content, "[current, pinned, archived]") {
		t.Fatalf("content = %q", content)
	}
}
