package session

import (
	"context"
	"elbot/internal/storage"
	"errors"
	"testing"
)

func TestServiceLifecycleOperations(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}

	session, err := svc.Create(ctx, scope, CreateRequest{Title: "lifecycle"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	archived, err := svc.Archive(ctx, scope, "")
	if err != nil {
		t.Fatalf("Archive current: %v", err)
	}
	if archived.ID != session.ID || archived.ArchivedAt == nil {
		t.Fatalf("archived = %#v", archived)
	}

	unarchived, err := svc.Unarchive(ctx, scope, "")
	if err != nil {
		t.Fatalf("Unarchive current: %v", err)
	}
	if unarchived.ArchivedAt != nil {
		t.Fatalf("unarchived still archived: %#v", unarchived)
	}

	pinned, err := svc.Pin(ctx, scope, session.ID)
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if pinned.PinnedAt == nil {
		t.Fatalf("pinned missing timestamp: %#v", pinned)
	}
	unpinned, err := svc.Unpin(ctx, scope, session.ID)
	if err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	if unpinned.PinnedAt != nil {
		t.Fatalf("unpinned still pinned: %#v", unpinned)
	}

	if err := svc.Delete(ctx, scope, session.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Sessions().Get(ctx, session.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get deleted = %v", err)
	}
	newSession, err := svc.GetOrCreateCurrent(ctx, scope, "after delete")
	if err != nil {
		t.Fatalf("GetOrCreateCurrent after delete: %v", err)
	}
	if newSession.ID == session.ID {
		t.Fatalf("current was not cleared after delete")
	}
}
