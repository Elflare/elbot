package sqlite

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/storage"
)

func TestMediaSignedURLMigrationAndLifecycle(t *testing.T) {
	ctx := t.Context()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.version >= 25 {
			break
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatal(err)
		}
	}
	r := &MediaRepository{db: db}
	item := &storage.Media{ID: "existing", Name: "old name", MIMEType: "image/png", Backend: "local"}
	if err := r.Upsert(ctx, item); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := runMigrations(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.GetSignedURL(ctx, item.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("initial cache: %v", err)
	}
	record := &storage.MediaSignedURL{MediaID: item.ID, URL: "https://media.example/one", ExpiresAt: storage.Now().Add(time.Hour)}
	if err := r.UpsertSignedURL(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.URL, record.ExpiresAt = "https://media.example/two", record.ExpiresAt.Add(time.Hour)
	if err := r.UpsertSignedURL(ctx, record); err != nil {
		t.Fatal(err)
	}
	item.Name = "new name"
	if err := r.Upsert(ctx, item); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetSignedURL(ctx, item.ID)
	if err != nil || got.URL != record.URL || !got.ExpiresAt.Equal(record.ExpiresAt) {
		t.Fatalf("metadata update changed cache: %+v %v", got, err)
	}
	if _, err := db.Exec(`DELETE FROM media WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetSignedURL(ctx, item.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cache survived media deletion: %v", err)
	}
	if err := r.UpsertSignedURL(ctx, record); err == nil {
		t.Fatal("cache accepted a missing media reference")
	}
}
