package sqlite

import (
	"context"
	"elbot/internal/storage"
	"errors"
	"strings"
	"testing"
)

func TestMessageMediaTransactionAndOwnerDeletion(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	session := &storage.Session{OwnerID: "u", Platform: "test", PlatformScopeID: "p"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	id := "media:" + strings.Repeat("a", 64)
	if err := store.Media().Upsert(ctx, &storage.Media{ID: id, MIMEType: "image/png", Size: 4, Backend: "local"}); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{storage.RoleUser, storage.RoleTool} {
		msg := &storage.Message{SessionID: session.ID, Role: role, Segments: `[{"type":"image","media":"` + id + `"},{"type":"image","media":"` + id + `"}]`}
		if err := store.Messages().Append(ctx, msg); err != nil {
			t.Fatal(err)
		}
		owner := "message"
		if role == storage.RoleTool {
			owner = "tool_result"
		}
		refs, err := store.MediaReferences().ListByOwner(ctx, owner, msg.ID)
		if err != nil || len(refs) != 1 {
			t.Fatalf("refs: %#v, %v", refs, err)
		}
		if role == storage.RoleUser {
			if _, err := store.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, msg.ID); err != nil {
				t.Fatal(err)
			}
			refs, err = store.MediaReferences().ListByOwner(ctx, owner, msg.ID)
			if err != nil || len(refs) != 0 {
				t.Fatalf("deleted owner refs: %#v %v", refs, err)
			}
		}
	}
	bad := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Segments: `[{"type":"image","media":"media:missing"}]`}
	if err := store.Messages().Append(ctx, bad); err == nil {
		t.Fatal("accepted dangling media")
	}
	if _, err := store.Messages().Get(ctx, bad.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("message not rolled back: %v", err)
	}
	if err := store.Sessions().Delete(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	refs, err := store.MediaReferences().ListMediaIDs(ctx, id)
	if err != nil || len(refs) != 0 {
		t.Fatalf("session refs not removed: %#v %v", refs, err)
	}
	if _, err := store.Media().Get(ctx, id); err != nil {
		t.Fatal("owner deletion removed media body", err)
	}
}
