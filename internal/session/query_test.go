package session

import (
	"context"
	"elbot/internal/storage"
	"testing"
	"time"
)

func TestServiceCreateCurrentResumeListStatus(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}

	first, err := svc.GetOrCreateCurrent(ctx, scope, "hello session")
	if err != nil {
		t.Fatalf("GetOrCreateCurrent: %v", err)
	}
	if first.Title != "hello session" {
		t.Fatalf("title = %q", first.Title)
	}

	second, err := svc.Create(ctx, scope, CreateRequest{Title: "second"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	current, err := svc.Current(ctx, scope)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current.ID != second.ID {
		t.Fatalf("current = %s, want %s", current.ID, second.ID)
	}

	aged := time.Now().Add(-time.Hour)
	first.UpdatedAt = aged
	if _, err := store.Sessions().Mutate(ctx, first.ID, func(latest *storage.Session) error { *latest = *first; return nil }); err != nil {
		t.Fatalf("age first session: %v", err)
	}
	if _, err := svc.Resume(ctx, scope, first.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	current, err = svc.Current(ctx, scope)
	if err != nil {
		t.Fatalf("Current after resume: %v", err)
	}
	if current.ID != first.ID {
		t.Fatalf("current = %s, want %s", current.ID, first.ID)
	}
	if !current.UpdatedAt.After(aged) {
		t.Fatalf("resumed updated_at = %v, want after %v", current.UpdatedAt, aged)
	}

	if err := store.Messages().Append(ctx, &storage.Message{SessionID: first.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: first.ID, Role: storage.RoleAssistant, Content: "hi"}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	status, err := svc.Status(ctx, scope)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.MessageCount != 2 || status.LastUserContent != "hello" || status.LastAnswerContent != "hi" {
		t.Fatalf("status = %#v", status)
	}

	list, err := svc.List(ctx, scope, "hello", 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != first.ID {
		t.Fatalf("list = %#v", list)
	}
}

func TestServiceGetOrCreateCurrentCreatesNewWhenNoInMemoryCurrent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	firstSvc := newTestSupervisor(t, store, nil)
	first, err := firstSvc.Create(ctx, scope, CreateRequest{Title: "old session"})
	if err != nil {
		t.Fatalf("create old session: %v", err)
	}

	secondSvc := newTestSupervisor(t, store, nil)
	second, err := secondSvc.GetOrCreateCurrent(ctx, scope, "new message")
	if err != nil {
		t.Fatalf("GetOrCreateCurrent: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("GetOrCreateCurrent resumed old session %s", first.ID)
	}
	if second.Title != "new message" {
		t.Fatalf("new session title = %q", second.Title)
	}
}

func TestServiceListResumablePageUsesRecentNonCurrentOrder(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	now := storage.Now()

	older, err := svc.Create(ctx, scope, CreateRequest{Title: "older pinned"})
	if err != nil {
		t.Fatalf("create older: %v", err)
	}
	older.UpdatedAt = now.Add(-2 * time.Hour)
	older.PinnedAt = &now
	if _, err := store.Sessions().Mutate(ctx, older.ID, func(latest *storage.Session) error { *latest = *older; return nil }); err != nil {
		t.Fatalf("update older: %v", err)
	}
	recent, err := svc.Create(ctx, scope, CreateRequest{Title: "recent"})
	if err != nil {
		t.Fatalf("create recent: %v", err)
	}
	recent.UpdatedAt = now.Add(-time.Hour)
	if _, err := store.Sessions().Mutate(ctx, recent.ID, func(latest *storage.Session) error { *latest = *recent; return nil }); err != nil {
		t.Fatalf("update recent: %v", err)
	}
	current, err := svc.Create(ctx, scope, CreateRequest{Title: "current"})
	if err != nil {
		t.Fatalf("create current: %v", err)
	}
	archived, err := svc.Create(ctx, scope, CreateRequest{Title: "archived"})
	if err != nil {
		t.Fatalf("create archived: %v", err)
	}
	if _, err := svc.Archive(ctx, scope, archived.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := svc.Resume(ctx, scope, current.ID); err != nil {
		t.Fatalf("resume current: %v", err)
	}

	got, err := svc.ListResumablePage(ctx, scope, 10, 0)
	if err != nil {
		t.Fatalf("ListResumablePage: %v", err)
	}
	if len(got) != 2 || got[0].ID != recent.ID || got[1].ID != older.ID {
		t.Fatalf("resumable sessions = %#v", got)
	}
}
