package session

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/storage"
	"errors"
	"testing"
	"time"
)

func TestListPageReturnsInvalidPageAndKeepsEmptyDefault(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}

	empty, err := svc.ListPage(ctx, scope, ListRequest{PageSize: 1})
	if err != nil || empty.Page != 1 || empty.TotalPages != 0 || len(empty.Items) != 0 {
		t.Fatalf("empty default page=%+v err=%v", empty, err)
	}
	if _, err := svc.ListPage(ctx, scope, ListRequest{Page: 2, PageSize: 1}); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("empty explicit page error=%v", err)
	}
	if _, err := svc.Create(ctx, scope, CreateRequest{Title: "one"}); err != nil {
		t.Fatal(err)
	}
	first, err := svc.ListPage(ctx, scope, ListRequest{PageSize: 1})
	if err != nil || first.Page != 1 || len(first.Items) != 1 || first.Items[0].Title != "one" {
		t.Fatalf("default page = %+v err=%v", first, err)
	}
	if _, err := svc.ListPage(ctx, scope, ListRequest{Page: 2, PageSize: 1}); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("non-empty explicit page error=%v", err)
	}
}

func TestServiceCreateCurrentResumeListStatus(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}

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

	if err := store.Messages().Append(ctx, &storage.Message{ID: storage.NewID(), SessionID: first.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{ID: storage.NewID(), SessionID: first.ID, Role: storage.RoleAssistant, Content: "hi"}); err != nil {
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

	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
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

func TestServiceListPageUsesPinnedNonCurrentOrder(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
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
	if _, err := svc.Pin(ctx, scope, current.ID); err != nil {
		t.Fatalf("pin current: %v", err)
	}

	got, err := svc.ListPage(ctx, scope, ListRequest{PageSize: 10, ExcludeCurrent: true})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(got.Items) != 2 || got.Items[0].ID != older.ID || got.Items[1].ID != recent.ID || got.TotalCount != 2 {
		t.Fatalf("resumable sessions = %#v", got)
	}
}
