package maindb

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func NewStoreForTest(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func CreateMediaForTest(t *testing.T, store *Store, id, mime string) {
	t.Helper()
	now := time.Now()
	if err := store.CreateMedia(t.Context(), &Media{ID: id, MIMEType: mime, Backend: "local", CreatedAt: now, LastAccessedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func hasSQLiteObject(t *testing.T, db *sql.DB, objectType, name string) bool {
	t.Helper()
	var found string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = ? AND name = ?`, objectType, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("query sqlite object %s %s: %v", objectType, name, err)
	}
	return true
}

func TestNewCreatesCurrentTables(t *testing.T) {
	store := NewStoreForTest(t)

	for _, table := range []string{"schema_migrations", "sessions", "messages", "platform_message_map", "context_summaries", "tool_call_records", "cron_jobs", "elnis_events", "elnis_report_deliveries", "media", "media_references"} {
		var name string
		err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}

	if hasSQLiteObject(t, store.db, "index", "idx_sessions_pinned_updated_at") {
		t.Fatal("pin should not have a dedicated index")
	}

	if hasSQLiteObject(t, store.db, "index", "idx_context_summaries_session_created_at") == false {
		t.Fatal("context summary session index missing")
	}

	var version int
	if err := store.db.QueryRow(`SELECT version FROM schema_migrations WHERE version = 12`).Scan(&version); err != nil {
		t.Fatalf("migration version missing: %v", err)
	}
}

func TestForkIndexesExist(t *testing.T) {
	store := NewStoreForTest(t)

	if !hasSQLiteObject(t, store.db, "index", "idx_sessions_parent") {
		t.Fatal("fork parent index missing")
	}
	if !hasSQLiteObject(t, store.db, "index", "idx_sessions_fork_from") {
		t.Fatal("fork from-message index missing")
	}
}
