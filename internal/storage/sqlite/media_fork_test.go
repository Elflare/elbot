package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"elbot/internal/storage"
)

func TestForkMediaReferencesRespectCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	parent := &storage.Session{OwnerID: "u", Platform: "test", PlatformScopeID: "s"}
	if err := store.Sessions().Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	var messages []*storage.Message
	for _, hash := range []string{"a", "b", "c"} {
		id := "media:" + strings.Repeat(hash, 64)
		if err := store.Media().Upsert(ctx, &storage.Media{ID: id, Backend: "local", MIMEType: "image/png"}); err != nil {
			t.Fatal(err)
		}
		msg := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, CreatedAt: at, Segments: fmt.Sprintf(`[{"type":"image","media":%q}]`, id)}
		if err := store.Messages().Append(ctx, msg); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, msg)
	}
	fork := &storage.Session{OwnerID: "u", Platform: "test", PlatformScopeID: "s", ParentSessionID: parent.ID, ForkFromMessageID: messages[1].ID}
	if err := store.Sessions().Create(ctx, fork); err != nil {
		t.Fatal(err)
	}
	assertHistory := func(id string) {
		t.Helper()
		refs, err := store.MediaReferences().ListByOwner(ctx, "session_fork", id)
		if err != nil || len(refs) != 2 {
			t.Fatalf("fork references outside checkpoint: %v %v", refs, err)
		}
		for _, ref := range refs {
			if ref.MediaID == "media:"+strings.Repeat("c", 64) {
				t.Fatal("retained media after fork checkpoint")
			}
		}
	}
	assertHistory(fork.ID)
	checkpoint := &storage.Message{SessionID: fork.ID, Role: storage.RoleAssistant, Content: "checkpoint"}
	if err := store.Messages().Append(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	nested := &storage.Session{OwnerID: "u", Platform: "test", PlatformScopeID: "s", ParentSessionID: fork.ID, ForkFromMessageID: checkpoint.ID}
	if err := store.Sessions().Create(ctx, nested); err != nil {
		t.Fatal(err)
	}
	assertHistory(nested.ID)
	if issues, err := store.Media().CheckReferences(ctx); err != nil || len(issues) != 0 {
		t.Fatalf("valid fork audit: %v %v", issues, err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM media_references WHERE owner_type='session_fork' AND owner_id=? AND media_id=?`, nested.ID, "media:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	issues, err := store.Media().CheckReferences(ctx)
	if err != nil || len(issues) != 1 || issues[0] != "missing fork reference: "+nested.ID {
		t.Fatalf("missing fork reference not detected: %v %v", issues, err)
	}
}

func TestForkInheritsSessionToolMediaBeforeCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	parent := &storage.Session{OwnerID: "u", Platform: "test", PlatformScopeID: "s"}
	if err := store.Sessions().Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	beforeID := "media:" + strings.Repeat("d", 64)
	afterID := "media:" + strings.Repeat("e", 64)
	for _, id := range []string{beforeID, afterID} {
		if err := store.Media().Upsert(ctx, &storage.Media{ID: id, Backend: "local", MIMEType: "image/png"}); err != nil {
			t.Fatal(err)
		}
	}
	checkpointAt := time.Now().UTC()
	checkpoint := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "checkpoint", CreatedAt: checkpointAt}
	if err := store.Messages().Append(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := store.MediaReferences().AddAll(ctx, []storage.MediaReference{
		{MediaID: beforeID, OwnerType: "session_tool", OwnerID: parent.ID, Purpose: "input", SessionID: parent.ID, CreatedAt: checkpointAt.Add(-time.Second)},
		{MediaID: afterID, OwnerType: "session_tool", OwnerID: parent.ID, Purpose: "input", SessionID: parent.ID, CreatedAt: checkpointAt.Add(time.Second)},
	}); err != nil {
		t.Fatal(err)
	}
	fork := &storage.Session{OwnerID: "u", Platform: "test", PlatformScopeID: "s", ParentSessionID: parent.ID, ForkFromMessageID: checkpoint.ID}
	if err := store.Sessions().Create(ctx, fork); err != nil {
		t.Fatal(err)
	}
	refs, err := store.MediaReferences().ListByOwner(ctx, "session_fork", fork.ID)
	if err != nil || len(refs) != 1 || refs[0].MediaID != beforeID {
		t.Fatalf("fork references = %#v, %v", refs, err)
	}
	if issues, err := store.Media().CheckReferences(ctx); err != nil || len(issues) != 0 {
		t.Fatalf("reference audit = %v, %v", issues, err)
	}
}
