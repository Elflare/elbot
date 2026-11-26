package maindb

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/storage/share"
)

func TestMediaSignedURLMigrationAndLifecycle(t *testing.T) {
	ctx := t.Context()
	db := OpenBeforeForTest(t, filepath.Join(t.TempDir(), "old.db")+"?_pragma=foreign_keys(1)", 25)
	r := &Store{db: db}
	item := &Media{ID: "existing", Name: "old name", CreatedAt: time.Now(), LastAccessedAt: time.Now(), MIMEType: "image/png", Backend: "local"}
	if err := r.CreateMedia(ctx, item); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := runMigrations(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.GetSignedURL(ctx, item.ID); !errors.Is(err, share.ErrNotFound) {
		t.Fatalf("initial cache: %v", err)
	}
	record := &MediaSignedURL{MediaID: item.ID, URL: "https://media.example/one", ExpiresAt: time.Now().Add(time.Hour)}
	if err := r.UpsertSignedURL(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.URL, record.ExpiresAt = "https://media.example/two", record.ExpiresAt.Add(time.Hour)
	if err := r.UpsertSignedURL(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMediaObjectKey(ctx, item.ID, "remote/key"); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetSignedURL(ctx, item.ID)
	if err != nil || got.URL != record.URL || !got.ExpiresAt.Equal(record.ExpiresAt) {
		t.Fatalf("metadata update changed cache: %+v %v", got, err)
	}
	if _, err := db.Exec(`DELETE FROM media WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetSignedURL(ctx, item.ID); !errors.Is(err, share.ErrNotFound) {
		t.Fatalf("cache survived media deletion: %v", err)
	}
	if err := r.UpsertSignedURL(ctx, record); err == nil {
		t.Fatal("cache accepted a missing media reference")
	}
}
