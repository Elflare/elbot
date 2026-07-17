package session

import (
	"context"
	"elbot/internal/storage"
	"errors"
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
	if status.MessageCount != 2 || status.LastUserPreview != "hello" || status.LastAnswerPreview != "hi" {
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

func TestPlatformScopeCanListAndResumeSamePlatformCronSessions(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "qq:user1", Platform: "qq-onebot", PlatformScopeID: "private:user1"}
	otherScope := Scope{ActorID: "qq:user1", Platform: "qq-onebot", PlatformScopeID: "group:g1"}

	front, err := svc.Create(ctx, scope, CreateRequest{Title: "front"})
	if err != nil {
		t.Fatalf("Create front: %v", err)
	}
	otherFront, err := svc.Create(ctx, otherScope, CreateRequest{Title: "other front"})
	if err != nil {
		t.Fatalf("Create other front: %v", err)
	}
	cronSession := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: "cron:user.cron.test", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "cron", Metadata: `{"discovered_tools":["web_search"]}`}
	if err := store.Sessions().Create(ctx, cronSession); err != nil {
		t.Fatalf("Create cron: %v", err)
	}
	otherPlatformCron := &storage.Session{OwnerID: scope.ActorID, Platform: "cli", PlatformScopeID: "cron:user.cron.test", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "cli cron", Metadata: `{"discovered_tools":["web_search"]}`}
	if err := store.Sessions().Create(ctx, otherPlatformCron); err != nil {
		t.Fatalf("Create other platform cron: %v", err)
	}

	list, err := svc.List(ctx, scope, "", 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := sessionIDs(list)
	if !ids[front.ID] || !ids[cronSession.ID] {
		t.Fatalf("expected front and cron sessions in list: %#v", list)
	}
	if ids[otherFront.ID] || ids[otherPlatformCron.ID] {
		t.Fatalf("unexpected cross-scope/platform session in list: %#v", list)
	}
	if _, err := svc.Resume(ctx, scope, cronSession.ID); err != nil {
		t.Fatalf("Resume cron: %v", err)
	}
	elnisSession := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: "elnis:event-1", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "elnis"}
	if err := store.Sessions().Create(ctx, elnisSession); err != nil {
		t.Fatalf("Create elnis: %v", err)
	}
	if _, err := svc.Resume(ctx, scope, elnisSession.ID); err != nil {
		t.Fatalf("Resume elnis: %v", err)
	}
	if _, err := svc.Resume(ctx, scope, otherFront.ID); err == nil {
		t.Fatal("expected other scope non-background session to be denied")
	}
}

func TestServiceCreateRequest(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}

	defaultSession, err := svc.Create(ctx, scope, CreateRequest{Metadata: `{"kind":"default"}`})
	if err != nil {
		t.Fatalf("Create default: %v", err)
	}
	if defaultSession.Title != "New session" || defaultSession.Mode != storage.SessionModeWork || defaultSession.Metadata != `{"kind":"default"}` {
		t.Fatalf("default session = %#v", defaultSession)
	}
	explicit, err := svc.Create(ctx, scope, CreateRequest{Title: "chat", Mode: storage.SessionModeChat, Metadata: `{"kind":"compact"}`})
	if err != nil {
		t.Fatalf("Create explicit: %v", err)
	}
	stored, err := store.Sessions().Get(ctx, explicit.ID)
	if err != nil {
		t.Fatalf("Get explicit: %v", err)
	}
	if stored.Mode != storage.SessionModeChat || stored.Metadata != `{"kind":"compact"}` {
		t.Fatalf("stored explicit session = %#v", stored)
	}
	if _, err := svc.Create(ctx, scope, CreateRequest{Title: "invalid", Mode: "invalid"}); err == nil {
		t.Fatal("expected invalid mode error")
	}
	current, err := svc.Current(ctx, scope)
	if err != nil || current.ID != explicit.ID {
		t.Fatalf("current after invalid create = %#v, err = %v", current, err)
	}
}

func TestServiceGetOrCreateCurrentCreatesNewWhenNoInMemoryCurrent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	firstSvc := NewService(store)
	first, err := firstSvc.Create(ctx, scope, CreateRequest{Title: "old session"})
	if err != nil {
		t.Fatalf("create old session: %v", err)
	}

	secondSvc := NewService(store)
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

func TestServiceGetOrCreateCurrentReturnsStorageError(t *testing.T) {
	base := newTestStore(t)
	wantErr := errors.New("session read failed")
	store := storeWithSessionRepository{Store: base, sessions: failingGetSessionRepository{SessionRepository: base.Sessions(), err: wantErr}}
	svc := NewService(store)
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	svc.setCurrent(scope, "current")

	if _, err := svc.GetOrCreateCurrent(context.Background(), scope, "must not create"); !errors.Is(err, wantErr) {
		t.Fatalf("GetOrCreateCurrent error = %v, want %v", err, wantErr)
	}
	sessions, err := base.Sessions().List(context.Background(), storage.ListSessionsRequest{IncludeAllPlatforms: true})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %#v, storage error should not create", sessions)
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
	if err := store.Sessions().Update(ctx, older); err != nil {
		t.Fatalf("update older: %v", err)
	}
	recent, err := svc.Create(ctx, scope, CreateRequest{Title: "recent"})
	if err != nil {
		t.Fatalf("create recent: %v", err)
	}
	recent.UpdatedAt = now.Add(-time.Hour)
	if err := store.Sessions().Update(ctx, recent); err != nil {
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

func TestServiceNonCLICannotResumeOtherScope(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	cliScope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	s, err := svc.Create(ctx, cliScope, CreateRequest{Title: "cli"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	qqScope := Scope{ActorID: "u1", Platform: "qq", PlatformScopeID: "group:1"}
	if _, err := svc.Resume(ctx, qqScope, s.ID); err == nil {
		t.Fatal("expected scope error")
	}
}
