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

func TestLifecycleCommandsArchiveAndArchives(t *testing.T) {
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
	selections := NewSessionCommandState(1, 30)
	selections.set(scope, []storage.SessionSummary{{ID: first.ID}})
	deps := Deps{
		Sessions:     svc,
		Requests:     request.NewManager(0),
		Turns:        turn.NewManager(),
		Store:        store,
		Scope:        func(context.Context) session.Scope { return scope },
		SessionState: selections,
	}

	preview, err := NewArchive(deps).Handle(ctx, command.Request{Args: "1"})
	if err != nil {
		t.Fatalf("archive preview: %v", err)
	}
	if !strings.Contains(preview.Content, "/archive "+first.ID+" --confirm") || !strings.Contains(preview.Content, "title: first") || !strings.Contains(preview.Content, "id: "+first.ID) {
		t.Fatalf("archive preview = %q", preview.Content)
	}
	selections.set(scope, []storage.SessionSummary{{ID: second.ID}})
	if _, err := NewArchive(deps).Handle(ctx, command.Request{Args: first.ID + " --confirm"}); err != nil {
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
	if !strings.Contains(deletePreview.Content, "/delete "+first.ID+" --confirm") {
		t.Fatalf("delete preview does not pin canonical id: %q", deletePreview.Content)
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

func TestArchiveCurrentConfirmationPinsSessionID(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)
	svc := session.NewService(store)
	scope := session.Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	current, err := svc.Create(ctx, scope, session.CreateRequest{Title: "current"})
	if err != nil {
		t.Fatalf("create current: %v", err)
	}
	deps := Deps{
		Sessions: svc,
		Store:    store,
		Scope:    func(context.Context) session.Scope { return scope },
	}

	preview, err := NewArchive(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("archive preview: %v", err)
	}
	if !strings.Contains(preview.Content, "/archive "+current.ID+" --confirm") {
		t.Fatalf("archive preview does not pin current id: %q", preview.Content)
	}

	replacement, err := svc.Create(ctx, scope, session.CreateRequest{Title: "replacement"})
	if err != nil {
		t.Fatalf("create replacement: %v", err)
	}
	if _, err := NewArchive(deps).Handle(ctx, command.Request{Args: current.ID + " --confirm"}); err != nil {
		t.Fatalf("archive pinned current: %v", err)
	}
	archived, err := svc.ListPage(ctx, scope, "", 10, 0, true)
	if err != nil {
		t.Fatalf("list archived: %v", err)
	}
	if len(archived) != 1 || archived[0].ID != current.ID {
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
	deps := Deps{Sessions: svc, Scope: func(context.Context) session.Scope { return scope }, SessionState: NewSessionCommandState(10, 30)}

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

func TestFormatSessionsShowsLifecycleMarkers(t *testing.T) {
	now := time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC)
	content := formatSessions([]storage.SessionSummary{{ID: "s1", Title: "one", UpdatedAt: now, PinnedAt: &now, ArchivedAt: &now}}, "s1")
	if !strings.Contains(content, "[current, pinned, archived]") {
		t.Fatalf("content = %q", content)
	}
}
