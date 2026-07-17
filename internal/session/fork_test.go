package session

import (
	"context"
	"elbot/internal/storage"
	"testing"
)

func TestServiceForkCreatesCurrentBranch(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	parent, err := svc.Create(ctx, scope, CreateRequest{Title: "parent session", Mode: storage.SessionModeChat})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	assistant := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	fork, err := svc.Fork(ctx, scope, assistant.ID)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if fork.ParentSessionID != parent.ID || fork.ForkFromMessageID != assistant.ID || fork.Mode != storage.SessionModeChat {
		t.Fatalf("fork = %#v", fork)
	}
	current, err := svc.Current(ctx, scope)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current.ID != fork.ID {
		t.Fatalf("current = %s, want %s", current.ID, fork.ID)
	}
}

func TestServiceForkRejectsNonAssistantMessage(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	session, err := svc.Create(ctx, scope, CreateRequest{Title: "parent"})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	user := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "question"}
	if err := store.Messages().Append(ctx, user); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if _, err := svc.Fork(ctx, scope, user.ID); err == nil {
		t.Fatal("expected non-assistant fork error")
	}
}

func TestServiceForkChecksScope(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	ownerScope := Scope{ActorID: "u1", Platform: "qq", PlatformScopeID: "group:1"}
	parent, err := svc.Create(ctx, ownerScope, CreateRequest{Title: "parent"})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	assistant := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	otherScope := Scope{ActorID: "u1", Platform: "qq", PlatformScopeID: "group:2"}
	if _, err := svc.Fork(ctx, otherScope, assistant.ID); err == nil {
		t.Fatal("expected scope error")
	}
	cliScope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	if _, err := svc.Fork(ctx, cliScope, assistant.ID); err != nil {
		t.Fatalf("CLI fork should be allowed: %v", err)
	}
}
