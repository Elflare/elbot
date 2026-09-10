package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/storage"
)

func TestHistoryMediaReferencesAndAtomicReplacement(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	repo := store.Media()
	id := "media:" + strings.Repeat("a", 64)
	if err := repo.Upsert(ctx, &storage.Media{ID: id, Backend: "local", MIMEType: "image/png"}); err != nil {
		t.Fatal(err)
	}
	item := storage.HistoryMedia{HistoryID: "history-1", Platform: "qqonebot", ScopeID: "group:1", MessageID: "message", Kind: "image", MediaID: id}
	for _, index := range []int{3, 1, 2} {
		item.MediaIndex = index
		if err := repo.SaveHistory(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.FindHistory(ctx, item.Platform, item.ScopeID, item.MessageID)
	if err != nil || len(got) != 3 {
		t.Fatalf("history = %#v %v", got, err)
	}
	for i, row := range got {
		if row.MediaIndex != i+1 || row.MediaID != id || row.OwnerID == "" {
			t.Fatalf("row = %#v", row)
		}
	}
	refs, err := store.MediaReferences().ListMediaIDs(ctx, id)
	if err != nil || len(refs) != 3 {
		t.Fatalf("refs = %#v %v", refs, err)
	}
	// Replacing with a missing object must roll back both the association and reference deletion.
	item.MediaIndex = 1
	item.MediaID = "media:" + strings.Repeat("b", 64)
	if err := repo.SaveHistory(ctx, item); err == nil {
		t.Fatal("missing media accepted")
	}
	got, err = repo.FindHistory(ctx, item.Platform, item.ScopeID, item.MessageID)
	if err != nil || len(got) != 3 || got[0].MediaID != id {
		t.Fatalf("replacement lost history: %#v %v", got, err)
	}
	item.MediaID = id
	if err := repo.SaveHistory(ctx, item); err != nil {
		t.Fatal(err)
	}
	refs, _ = store.MediaReferences().ListMediaIDs(ctx, id)
	if len(refs) != 3 {
		t.Fatalf("duplicate save refs = %#v", refs)
	}
	other, err := repo.FindHistory(ctx, item.Platform, "group:2", item.MessageID)
	if err != nil || len(other) != 0 {
		t.Fatalf("scope leak: %#v %v", other, err)
	}
	if err := store.MediaReferences().Add(ctx, &storage.MediaReference{MediaID: id, OwnerType: "test", OwnerID: "independent", Purpose: "content"}); err != nil {
		t.Fatal(err)
	}
	page, err := repo.ListHistory(ctx, "", 2)
	if err != nil || len(page) != 2 {
		t.Fatalf("page = %#v %v", page, err)
	}
	tail, err := repo.ListHistory(ctx, page[1].OwnerID, 2)
	if err != nil || len(tail) != 1 {
		t.Fatalf("tail = %#v %v", tail, err)
	}
	for _, row := range append(page, tail...) {
		if err := repo.DeleteHistory(ctx, row.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	refs, _ = store.MediaReferences().ListMediaIDs(ctx, id)
	if len(refs) != 1 || refs[0].OwnerID != "independent" {
		t.Fatalf("independent refs = %#v", refs)
	}
	if objects, err := repo.ClaimOrphans(ctx, time.Now()); err != nil || len(objects) != 0 {
		t.Fatalf("referenced object claimed: %#v %v", objects, err)
	}
	if err := store.MediaReferences().Remove(ctx, refs[0]); err != nil {
		t.Fatal(err)
	}
	if objects, err := repo.ClaimOrphans(ctx, time.Now().Add(-time.Hour)); err != nil || len(objects) != 0 {
		t.Fatalf("grace ignored: %#v %v", objects, err)
	}
	if issues, err := repo.CheckReferences(ctx); err != nil || len(issues) != 0 {
		t.Fatalf("audit = %#v %v", issues, err)
	}
}

func TestHistoryMediaRejectsInvalidPosition(t *testing.T) {
	repo := newTestStore(t).Media()
	for _, index := range []int{-1, 0} {
		if err := repo.SaveHistory(context.Background(), storage.HistoryMedia{HistoryID: "h", Platform: "p", ScopeID: "s", MessageID: "m", MediaIndex: index, Kind: "image", MediaID: "media:" + strings.Repeat("a", 64)}); err == nil {
			t.Fatalf("accepted index %d", index)
		}
	}
}
