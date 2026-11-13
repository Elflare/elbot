package session

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"errors"
	"testing"
)

func TestServiceLifecycleOperations(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", ChatType: "private"}

	created, err := svc.Create(ctx, scope, CreateRequest{Title: "lifecycle"})
	if err != nil || created.Status != operationresult.Success {
		t.Fatalf("Create: %+v %v", created, err)
	}
	session := created.Data
	archivedResult, err := svc.Archive(ctx, scope, "")
	if err != nil || archivedResult.Status != operationresult.Success {
		t.Fatalf("Archive current: %+v %v", archivedResult, err)
	}
	archived := archivedResult.Data
	if archived.ID != session.ID || archived.ArchivedAt == nil {
		t.Fatalf("archived = %#v", archived)
	}

	unarchivedResult, err := svc.Unarchive(ctx, scope, "")
	if err != nil || unarchivedResult.Status != operationresult.Success {
		t.Fatalf("Unarchive current: %+v %v", unarchivedResult, err)
	}
	unarchived := unarchivedResult.Data
	if unarchived.ArchivedAt != nil {
		t.Fatalf("unarchived still archived: %#v", unarchived)
	}

	pinnedResult, err := svc.Pin(ctx, scope, session.ID)
	if err != nil || pinnedResult.Status != operationresult.Success {
		t.Fatalf("Pin: %+v %v", pinnedResult, err)
	}
	pinned := pinnedResult.Data
	if pinned.PinnedAt == nil {
		t.Fatalf("pinned missing timestamp: %#v", pinned)
	}
	unpinnedResult, err := svc.Unpin(ctx, scope, session.ID)
	if err != nil || unpinnedResult.Status != operationresult.Success {
		t.Fatalf("Unpin: %+v %v", unpinnedResult, err)
	}
	unpinned := unpinnedResult.Data
	if unpinned.PinnedAt != nil {
		t.Fatalf("unpinned still pinned: %#v", unpinned)
	}

	if result, err := svc.Delete(ctx, scope, session.ID); err != nil || result.Status != operationresult.Success {
		t.Fatalf("Delete: %+v %v", result, err)
	}
	if _, err := store.Sessions().Get(ctx, session.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get deleted = %v", err)
	}
	newSession, err := svc.GetOrCreateCurrent(ctx, scope, "after delete")
	if err != nil || newSession.Status != operationresult.Success {
		t.Fatalf("GetOrCreateCurrent after delete: %+v %v", newSession, err)
	}
	if newSession.Data.ID == session.ID {
		t.Fatalf("current was not cleared after delete")
	}
}
