package session

import (
	"context"
	"elbot/internal/storage"
	"errors"
	"testing"
)

func TestServiceActivateModeCreatesAndReusesModeSession(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	activated, err := svc.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat, NewSessionTitle: "chat"})
	if err != nil {
		t.Fatalf("ActivateMode create: %v", err)
	}
	if activated.AlreadyActive {
		t.Fatalf("created mode reported already active: %#v", activated)
	}
	if activated.Session.Mode != storage.SessionModeChat || activated.Session.Title != "chat" {
		t.Fatalf("created session = %#v", activated.Session)
	}

	again, err := svc.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat})
	if err != nil {
		t.Fatalf("ActivateMode again: %v", err)
	}
	if !again.AlreadyActive || again.Session.ID != activated.Session.ID {
		t.Fatalf("again = %#v", again)
	}
	loaded, err := store.Sessions().Get(ctx, activated.Session.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Mode != storage.SessionModeChat {
		t.Fatalf("persisted mode = %q", loaded.Mode)
	}
	if _, err := svc.ActivateMode(ctx, scope, ActivateModeRequest{Mode: "invalid"}); err == nil {
		t.Fatal("expected invalid mode error")
	}
}

func TestServiceActivateModeChangesEmptySession(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	work, err := svc.Create(ctx, scope, CreateRequest{Title: "mode test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	activated, err := svc.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat})
	if err != nil {
		t.Fatalf("ActivateMode: %v", err)
	}
	if activated.AlreadyActive || activated.Session.ID != work.ID {
		t.Fatalf("activated = %#v", activated)
	}
}

func TestServiceActivateChatRejectsWorkSessionWithHistory(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	work, err := svc.Create(ctx, scope, CreateRequest{Title: "mode test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: work.ID, Role: storage.RoleUser, Content: "history"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_, err = svc.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat})
	if !errors.Is(err, ErrChatModeRequiresEmptySession) {
		t.Fatalf("ActivateMode error = %v", err)
	}
	loaded, err := store.Sessions().Get(ctx, work.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Mode != storage.SessionModeWork {
		t.Fatalf("persisted mode = %q", loaded.Mode)
	}
}
