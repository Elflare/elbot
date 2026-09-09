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
