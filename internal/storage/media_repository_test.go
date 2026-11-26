package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMediaRepositoryAndReferences(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := context.Background()
	createdAt := Now().Add(-time.Minute)
	media := &Media{
		ID:             "media:abc123",
		Name:           "cat.png",
		MIMEType:       "image/png",
		Size:           42,
		Backend:        "local",
		LocalPath:      filepath.Join(t.TempDir(), "cat.png"),
		SourcePlatform: "telegram",
		SourceFileID:   "file-1",
		CreatedAt:      createdAt,
	}
	if err := store.UpsertMedia(ctx, media); err != nil {
		t.Fatalf("upsert media: %v", err)
	}
	got, err := store.GetMedia(ctx, media.ID)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if got.ID != media.ID || got.Name != media.Name || got.MIMEType != media.MIMEType || got.Size != media.Size || got.LocalPath != media.LocalPath {
		t.Fatalf("media = %#v", got)
	}

	ref := &MediaReference{MediaID: media.ID, OwnerType: "message", OwnerID: "message-1", Purpose: "input", SessionID: "session-1"}
	if err := store.AddMediaReference(ctx, ref); err != nil {
		t.Fatalf("add reference: %v", err)
	}
	if err := store.AddMediaReference(ctx, ref); err != nil {
		t.Fatalf("add duplicate reference: %v", err)
	}
	refs, err := store.ListByOwner(ctx, ref.OwnerType, ref.OwnerID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("owner references = %#v, err=%v", refs, err)
	}
	refs, err = store.ListMediaIDs(ctx, media.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("media references = %#v, err=%v", refs, err)
	}
	if err := store.RemoveMediaReference(ctx, *ref); err != nil {
		t.Fatalf("remove reference: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM media WHERE id = ?`, media.ID); err != nil {
		t.Fatalf("delete media: %v", err)
	}
	if refs, err := store.ListMediaIDs(ctx, media.ID); err != nil || len(refs) != 0 {
		t.Fatalf("references after media delete = %#v, err=%v", refs, err)
	}
}
