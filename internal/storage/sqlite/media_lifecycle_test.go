package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"elbot/internal/storage"
)

func TestSharedMediaSessionsForkAndCleanupClaim(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	id := "media:" + strings.Repeat("a", 64)
	if err := store.Media().Upsert(ctx, &storage.Media{ID: id, Backend: "local", MIMEType: "image/png", CreatedAt: time.Now().Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var checkpoint string
	sessions := make([]storage.Session, 2)
	for i := range sessions {
		sessions[i] = storage.Session{OwnerID: "u", Platform: "p", PlatformScopeID: "s"}
		if err := store.Sessions().Create(ctx, &sessions[i]); err != nil {
			t.Fatal(err)
		}
		msg := &storage.Message{SessionID: sessions[i].ID, Role: storage.RoleUser, Segments: fmt.Sprintf(`[{"type":"image","media":%q}]`, id)}
		if err := store.Messages().Append(ctx, msg); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			checkpoint = msg.ID
		}
	}
	fork := &storage.Session{OwnerID: "u", Platform: "p", PlatformScopeID: "s", ParentSessionID: sessions[0].ID, ForkFromMessageID: checkpoint}
	if err := store.Sessions().Create(ctx, fork); err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		if err := store.Sessions().Delete(ctx, session.ID); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := store.MediaReferences().ListMediaIDs(ctx, id)
	if err != nil || len(refs) != 1 || refs[0].OwnerType != "session_fork" {
		t.Fatalf("fork refs %v %v", refs, err)
	}
	if items, err := store.Media().ClaimOrphans(ctx, time.Now()); err != nil || len(items) != 0 {
		t.Fatalf("claimed shared media %v %v", items, err)
	}
	if err := store.Sessions().Delete(ctx, fork.ID); err != nil {
		t.Fatal(err)
	}
	if items, err := store.Media().ClaimOrphans(ctx, time.Now().Add(-time.Hour)); err != nil || len(items) != 0 {
		t.Fatalf("grace starts too early %v %v", items, err)
	}
	if items, err := store.Media().ClaimOrphans(ctx, time.Now().Add(time.Hour)); err != nil || len(items) != 1 {
		t.Fatalf("claim %v %v", items, err)
	}
	if err := store.MediaReferences().Add(ctx, &storage.MediaReference{MediaID: id, OwnerType: "test", OwnerID: "late", Purpose: "input"}); err == nil {
		t.Fatal("reference added after cleanup claim")
	}
	if err := store.Media().FinishDelete(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestOutputMediaScopeExpiryAndDedup(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Now()
	var ids []string
	for _, ch := range []string{"a", "b"} {
		id := "media:" + strings.Repeat(ch, 64)
		ids = append(ids, id)
		if err := store.Media().Upsert(ctx, &storage.Media{ID: id, Backend: "local", MIMEType: "image/png"}); err != nil {
			t.Fatal(err)
		}
		out := storage.MediaOutput{Platform: "p", ScopeID: "group:1", MessageID: "m", MediaID: id, ExpiresAt: now.Add(time.Hour)}
		for range 2 {
			if err := store.Media().SaveOutput(ctx, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := store.Media().FindOutputs(ctx, "p", "group:1", "m", now)
	if err != nil || len(got) != 2 {
		t.Fatalf("outputs %v %v", got, err)
	}
	for _, scope := range []string{"group:2", "private:1"} {
		got, err := store.Media().FindOutputs(ctx, "p", scope, "m", now)
		if err != nil || len(got) != 0 {
			t.Fatalf("scope leak %v %v", got, err)
		}
	}
	if err := store.MediaReferences().Add(ctx, &storage.MediaReference{MediaID: ids[0], OwnerType: "test", OwnerID: "session", Purpose: "content"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Media().ExpireOutputs(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	refs, _ := store.MediaReferences().ListMediaIDs(ctx, ids[0])
	if len(refs) != 1 {
		t.Fatalf("independent owner lost %v", refs)
	}
	refs, _ = store.MediaReferences().ListMediaIDs(ctx, ids[1])
	if len(refs) != 0 {
		t.Fatalf("cache leaked %v", refs)
	}
}

func TestReferenceAndCleanupRace(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	for i := range 12 {
		id := fmt.Sprintf("media:%064x", i)
		if err := store.Media().Upsert(ctx, &storage.Media{ID: id, Backend: "local", MIMEType: "text/plain", CreatedAt: time.Now().Add(-24 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		ready := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			<-ready
			result <- store.MediaReferences().Add(ctx, &storage.MediaReference{MediaID: id, OwnerType: "test", OwnerID: id, Purpose: "content"})
		}()
		close(ready)
		if _, err := store.Media().ClaimOrphans(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		addErr := <-result
		item, err := store.Media().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		refs, err := store.MediaReferences().ListMediaIDs(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Deleting && len(refs) != 0 || !item.Deleting && (len(refs) != 1 || addErr != nil) {
			t.Fatalf("race: deleting=%v refs=%v err=%v", item.Deleting, refs, addErr)
		}
	}
}

func TestReferenceAuditAndRecovery(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	id := "media:" + strings.Repeat("c", 64)
	if err := store.Media().Upsert(ctx, &storage.Media{ID: id, Backend: "local", MIMEType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"hook", "skill", "request"} {
		if err := store.MediaReferences().Add(ctx, &storage.MediaReference{MediaID: id, OwnerType: kind, OwnerID: kind, Purpose: "temporary"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Media().RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	refs, _ := store.MediaReferences().ListMediaIDs(ctx, id)
	if len(refs) != 0 {
		t.Fatal(refs)
	}
	s := &storage.Session{OwnerID: "u", Platform: "p", PlatformScopeID: "s"}
	if err := store.Sessions().Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	msg := &storage.Message{SessionID: s.ID, Role: storage.RoleUser, Segments: fmt.Sprintf(`[{"type":"image","media":%q}]`, id)}
	if err := store.Messages().Append(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if issues, err := store.Media().CheckReferences(ctx); err != nil || len(issues) != 0 {
		t.Fatalf("audit %v %v", issues, err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM media_references WHERE owner_id=?`, msg.ID); err != nil {
		t.Fatal(err)
	}
	if issues, err := store.Media().CheckReferences(ctx); err != nil || len(issues) != 1 {
		t.Fatalf("missed corruption %v %v", issues, err)
	}
}
