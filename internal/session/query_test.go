package session

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"testing"
	"time"
)

func TestListPageReturnsInvalidPageAndKeepsEmptyDefault(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", ChatType: "private"}

	empty, err := svc.ListPage(ctx, scope, ListRequest{PageSize: 1})
	if err != nil || empty.Status != operationresult.Success || empty.Data.Page != 1 || empty.Data.TotalPages != 0 || len(empty.Data.Items) != 0 {
		t.Fatalf("empty default page=%+v err=%v", empty, err)
	}
	if result, err := svc.ListPage(ctx, scope, ListRequest{Page: 2, PageSize: 1}); err != nil || result.Status != operationresult.InvalidArguments {
		t.Fatalf("empty explicit page=%+v err=%v", result, err)
	}
	if result, err := svc.Create(ctx, scope, CreateRequest{Title: "one"}); err != nil || result.Status != operationresult.Success {
		t.Fatalf("create: %+v %v", result, err)
	}
	first, err := svc.ListPage(ctx, scope, ListRequest{PageSize: 1})
	if err != nil || first.Status != operationresult.Success || first.Data.Page != 1 || len(first.Data.Items) != 1 || first.Data.Items[0].Title != "one" {
		t.Fatalf("default page = %+v err=%v", first, err)
	}
	if result, err := svc.ListPage(ctx, scope, ListRequest{Page: 2, PageSize: 1}); err != nil || result.Status != operationresult.InvalidArguments {
		t.Fatalf("non-empty explicit page=%+v err=%v", result, err)
	}
}

func TestCLIListDefaultsToCurrentPlatformUntilExplicitAllPlatforms(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	current := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "admin"}, Platform: "cli", ChatID: "local", ChatType: contextinfo.ChatPrivate}
	foreign := Scope{Owner: contextinfo.ActorIdentity{Platform: "telegram", ID: "user"}, Platform: "telegram", ChatID: "chat", ChatType: contextinfo.ChatPrivate}
	if result, err := svc.Create(ctx, current, CreateRequest{Title: "cli"}); err != nil || result.Status != operationresult.Success {
		t.Fatalf("create local: %+v %v", result, err)
	}
	if result, err := svc.Create(ctx, foreign, CreateRequest{Title: "telegram"}); err != nil || result.Status != operationresult.Success {
		t.Fatalf("create foreign: %+v %v", result, err)
	}
	local, err := svc.ListPage(ctx, current, ListRequest{PageSize: 10})
	if err != nil || local.Status != operationresult.Success || len(local.Data.Items) != 1 || local.Data.Items[0].Platform != "cli" {
		t.Fatalf("default CLI list = %+v, %v", local, err)
	}
	all, err := svc.ListPage(ctx, current, ListRequest{PageSize: 10, AllPlatforms: true})
	if err != nil || all.Status != operationresult.Success || len(all.Data.Items) != 2 {
		t.Fatalf("all-platform list = %+v, %v", all, err)
	}
}

func TestServiceCreateCurrentResumeListStatus(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", ChatType: "private"}

	firstResult, err := svc.GetOrCreateCurrent(ctx, scope, "hello session")
	if err != nil || firstResult.Status != operationresult.Success {
		t.Fatalf("GetOrCreateCurrent: %+v %v", firstResult, err)
	}
	first := firstResult.Data
	if first.Title != "hello session" {
		t.Fatalf("title = %q", first.Title)
	}

	secondResult, err := svc.Create(ctx, scope, CreateRequest{Title: "second"})
	if err != nil || secondResult.Status != operationresult.Success {
		t.Fatalf("Create: %+v %v", secondResult, err)
	}
	second := secondResult.Data
	current, err := svc.Current(ctx, scope)
	if err != nil || current.Status != operationresult.Success {
		t.Fatalf("Current: %+v %v", current, err)
	}
	if current.Data.ID != second.ID {
		t.Fatalf("current = %s, want %s", current.Data.ID, second.ID)
	}

	aged := time.Now().Add(-time.Hour)
	first.UpdatedAt = aged
	if _, err := store.Sessions().Mutate(ctx, first.ID, func(latest *storage.Session) error { *latest = *first; return nil }); err != nil {
		t.Fatalf("age first session: %v", err)
	}
	if result, err := svc.Resume(ctx, scope, first.ID); err != nil || result.Status != operationresult.Success {
		t.Fatalf("Resume: %+v %v", result, err)
	}
	current, err = svc.Current(ctx, scope)
	if err != nil || current.Status != operationresult.Success {
		t.Fatalf("Current after resume: %+v %v", current, err)
	}
	if current.Data.ID != first.ID {
		t.Fatalf("current = %s, want %s", current.Data.ID, first.ID)
	}
	if !current.Data.UpdatedAt.After(aged) {
		t.Fatalf("resumed updated_at = %v, want after %v", current.Data.UpdatedAt, aged)
	}

	if err := store.Messages().Append(ctx, &storage.Message{ID: storage.NewID(), SessionID: first.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{ID: storage.NewID(), SessionID: first.ID, Role: storage.RoleAssistant, Content: "hi"}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	status, err := svc.Status(ctx, scope)
	if err != nil || status.Status != operationresult.Success {
		t.Fatalf("Status: %+v %v", status, err)
	}
	if status.Data.MessageCount != 2 || status.Data.LastUserContent != "hello" || status.Data.LastAnswerContent != "hi" {
		t.Fatalf("status = %#v", status.Data)
	}

	list, err := svc.List(ctx, scope, "hello", 20)
	if err != nil || list.Status != operationresult.Success {
		t.Fatalf("List: %+v %v", list, err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != first.ID {
		t.Fatalf("list = %#v", list.Data)
	}
}

func TestServiceGetOrCreateCurrentCreatesNewWhenNoInMemoryCurrent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", ChatType: "private"}
	firstSvc := newTestSupervisor(t, store, nil)
	firstResult, err := firstSvc.Create(ctx, scope, CreateRequest{Title: "old session"})
	if err != nil || firstResult.Status != operationresult.Success {
		t.Fatalf("create old session: %+v %v", firstResult, err)
	}
	first := firstResult.Data

	secondSvc := newTestSupervisor(t, store, nil)
	secondResult, err := secondSvc.GetOrCreateCurrent(ctx, scope, "new message")
	if err != nil || secondResult.Status != operationresult.Success {
		t.Fatalf("GetOrCreateCurrent: %+v %v", secondResult, err)
	}
	second := secondResult.Data
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
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", ChatType: "private"}
	now := storage.Now()

	olderResult, err := svc.Create(ctx, scope, CreateRequest{Title: "older pinned"})
	if err != nil || olderResult.Status != operationresult.Success {
		t.Fatalf("create older: %+v %v", olderResult, err)
	}
	older := olderResult.Data
	older.UpdatedAt = now.Add(-2 * time.Hour)
	older.PinnedAt = &now
	if _, err := store.Sessions().Mutate(ctx, older.ID, func(latest *storage.Session) error { *latest = *older; return nil }); err != nil {
		t.Fatalf("update older: %v", err)
	}
	recentResult, err := svc.Create(ctx, scope, CreateRequest{Title: "recent"})
	if err != nil || recentResult.Status != operationresult.Success {
		t.Fatalf("create recent: %+v %v", recentResult, err)
	}
	recent := recentResult.Data
	recent.UpdatedAt = now.Add(-time.Hour)
	if _, err := store.Sessions().Mutate(ctx, recent.ID, func(latest *storage.Session) error { *latest = *recent; return nil }); err != nil {
		t.Fatalf("update recent: %v", err)
	}
	currentResult, err := svc.Create(ctx, scope, CreateRequest{Title: "current"})
	if err != nil || currentResult.Status != operationresult.Success {
		t.Fatalf("create current: %+v %v", currentResult, err)
	}
	current := currentResult.Data
	archivedResult, err := svc.Create(ctx, scope, CreateRequest{Title: "archived"})
	if err != nil || archivedResult.Status != operationresult.Success {
		t.Fatalf("create archived: %+v %v", archivedResult, err)
	}
	archived := archivedResult.Data
	if result, err := svc.Archive(ctx, scope, archived.ID); err != nil || result.Status != operationresult.Success {
		t.Fatalf("archive: %+v %v", result, err)
	}
	if result, err := svc.Resume(ctx, scope, current.ID); err != nil || result.Status != operationresult.Success {
		t.Fatalf("resume current: %+v %v", result, err)
	}
	if result, err := svc.Pin(ctx, scope, current.ID); err != nil || result.Status != operationresult.Success {
		t.Fatalf("pin current: %+v %v", result, err)
	}

	got, err := svc.ListPage(ctx, scope, ListRequest{PageSize: 10, ExcludeCurrent: true})
	if err != nil || got.Status != operationresult.Success {
		t.Fatalf("ListPage: %+v %v", got, err)
	}
	if len(got.Data.Items) != 2 || got.Data.Items[0].ID != older.ID || got.Data.Items[1].ID != recent.ID || got.Data.TotalCount != 2 {
		t.Fatalf("resumable sessions = %#v", got.Data)
	}
}
