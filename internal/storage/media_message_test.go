package storage

import (
	"context"
	"elbot/internal/contextinfo"
	"errors"
	"strings"
	"testing"
)

func TestMessageMediaTransactionAndOwnerDeletion(t *testing.T) {
	ctx := context.Background()
	store := NewStoreForTest(t)
	session := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "test", ID: "u"}, Platform: "test", ChatID: "p"}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	id := "media:" + strings.Repeat("a", 64)
	if err := store.UpsertMedia(ctx, &Media{ID: id, MIMEType: "image/png", Size: 4, Backend: "local"}); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{RoleUser, RoleTool} {
		msg := &Message{ID: NewID(), SessionID: session.ID, Role: role, Segments: `[{"type":"image","media":"` + id + `"},{"type":"image","media":"` + id + `"}]`}
		if err := store.AppendMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
		owner := "message"
		if role == RoleTool {
			owner = "tool_result"
		}
		refs, err := store.ListByOwner(ctx, owner, msg.ID)
		if err != nil || len(refs) != 1 {
			t.Fatalf("refs: %#v, %v", refs, err)
		}
		if role == RoleUser {
			if _, err := store.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, msg.ID); err != nil {
				t.Fatal(err)
			}
			refs, err = store.ListByOwner(ctx, owner, msg.ID)
			if err != nil || len(refs) != 0 {
				t.Fatalf("deleted owner refs: %#v %v", refs, err)
			}
		}
	}
	bad := &Message{ID: NewID(), SessionID: session.ID, Role: RoleUser, Segments: `[{"type":"image","media":"media:missing"}]`}
	if err := store.AppendMessage(ctx, bad); err == nil {
		t.Fatal("accepted dangling media")
	}
	if _, err := store.GetMessage(ctx, bad.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message not rolled back: %v", err)
	}
	if err := store.DeleteSession(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	refs, err := store.ListMediaIDs(ctx, id)
	if err != nil || len(refs) != 0 {
		t.Fatalf("session refs not removed: %#v %v", refs, err)
	}
	if _, err := store.GetMedia(ctx, id); err != nil {
		t.Fatal("owner deletion removed media body", err)
	}
}
