package storage

import (
	"context"
	"elbot/internal/contextinfo"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestForkMediaReferencesRespectCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := NewStoreForTest(t)
	parent := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "test", ID: "u"}, Platform: "test", ChatID: "s"}
	if err := store.CreateSession(ctx, parent); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	var messages []*Message
	for _, hash := range []string{"a", "b", "c"} {
		id := "media:" + strings.Repeat(hash, 64)
		if err := store.UpsertMedia(ctx, &Media{ID: id, Backend: "local", MIMEType: "image/png"}); err != nil {
			t.Fatal(err)
		}
		msg := &Message{ID: NewID(), SessionID: parent.ID, Role: RoleAssistant, CreatedAt: at, Segments: fmt.Sprintf(`[{"type":"image","media":%q}]`, id)}
		if err := store.AppendMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, msg)
	}
	fork := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "test", ID: "u"}, Platform: "test", ChatID: "s", ParentSessionID: parent.ID, ForkFromMessageID: messages[1].ID}
	if err := store.CreateSession(ctx, fork); err != nil {
		t.Fatal(err)
	}
	assertHistory := func(id string) {
		t.Helper()
		refs, err := store.ListByOwner(ctx, "session_fork", id)
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
	checkpoint := &Message{ID: NewID(), SessionID: fork.ID, Role: RoleAssistant, Content: "checkpoint"}
	if err := store.AppendMessage(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	nested := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "test", ID: "u"}, Platform: "test", ChatID: "s", ParentSessionID: fork.ID, ForkFromMessageID: checkpoint.ID}
	if err := store.CreateSession(ctx, nested); err != nil {
		t.Fatal(err)
	}
	assertHistory(nested.ID)
	if issues, err := store.CheckReferences(ctx); err != nil || len(issues) != 0 {
		t.Fatalf("valid fork audit: %v %v", issues, err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM media_references WHERE owner_type='session_fork' AND owner_id=? AND media_id=?`, nested.ID, "media:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	issues, err := store.CheckReferences(ctx)
	if err != nil || len(issues) != 1 || issues[0] != "issue=missing_reference owner_type=session_fork owner_id="+nested.ID+" session_id="+nested.ID+" media_id=media:"+strings.Repeat("a", 64) {
		t.Fatalf("missing fork reference not detected: %v %v", issues, err)
	}
}

func TestForkInheritsSessionToolMediaBeforeCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := NewStoreForTest(t)
	parent := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "test", ID: "u"}, Platform: "test", ChatID: "s"}
	if err := store.CreateSession(ctx, parent); err != nil {
		t.Fatal(err)
	}
	beforeID := "media:" + strings.Repeat("d", 64)
	afterID := "media:" + strings.Repeat("e", 64)
	for _, id := range []string{beforeID, afterID} {
		if err := store.UpsertMedia(ctx, &Media{ID: id, Backend: "local", MIMEType: "image/png"}); err != nil {
			t.Fatal(err)
		}
	}
	checkpointAt := time.Now().UTC()
	checkpoint := &Message{ID: NewID(), SessionID: parent.ID, Role: RoleAssistant, Content: "checkpoint", CreatedAt: checkpointAt}
	if err := store.AppendMessage(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMediaReferences(ctx, []MediaReference{
		{MediaID: beforeID, OwnerType: "session_tool", OwnerID: parent.ID, Purpose: "input", SessionID: parent.ID, CreatedAt: checkpointAt.Add(-time.Second)},
		{MediaID: afterID, OwnerType: "session_tool", OwnerID: parent.ID, Purpose: "input", SessionID: parent.ID, CreatedAt: checkpointAt.Add(time.Second)},
	}); err != nil {
		t.Fatal(err)
	}
	fork := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "test", ID: "u"}, Platform: "test", ChatID: "s", ParentSessionID: parent.ID, ForkFromMessageID: checkpoint.ID}
	if err := store.CreateSession(ctx, fork); err != nil {
		t.Fatal(err)
	}
	refs, err := store.ListByOwner(ctx, "session_fork", fork.ID)
	if err != nil || len(refs) != 1 || refs[0].MediaID != beforeID {
		t.Fatalf("fork references = %#v, %v", refs, err)
	}
	if issues, err := store.CheckReferences(ctx); err != nil || len(issues) != 0 {
		t.Fatalf("reference audit = %v, %v", issues, err)
	}
}
