package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"elbot/internal/contextinfo"
)

func NewStoreForTest(t *testing.T) *Supervisor {
	t.Helper()
	store, err := NewSupervisor(context.Background(), filepath.Join(t.TempDir(), "elbot_sessions.db"), ":memory:")
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
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

func TestNewCanRunTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elbot_sessions.db")
	first, err := NewSupervisor(context.Background(), path, ":memory:")
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	session := &Session{ID: "fresh", Owner: contextinfo.ActorIdentity{Platform: "qq", ID: "u1"}, Platform: "qq", ChatType: "group", ChatID: "1", Title: "keep title", Metadata: `{"keep":true}`}
	if err := first.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second, err := NewSupervisor(context.Background(), path, ":memory:")
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer second.Close()
	got, err := second.GetSession(t.Context(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != session.Owner || got.Platform != session.Platform || got.Title != session.Title || got.Metadata != session.Metadata {
		t.Fatalf("reopening changed stored session or replayed historical conversions: %+v", got)
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
