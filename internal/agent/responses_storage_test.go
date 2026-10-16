package agent

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"

	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func nativeWindowRecords(t *testing.T, store storage.Store, sessionID string) *storage.NativeWindowRecords {
	t.Helper()
	row, err := store.Sessions().Get(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.Dialogues().CurrentCheckpoint(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Dialogues().LoadNativeWindow(t.Context(), row, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

// Failed, canceled and compaction exchanges have no checkpoint in the current
// window. Read their persisted audit fields without extending the runtime API.
func newNativeAuditDatabase(t *testing.T) (storage.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := sqlite.New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dsn := &url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store, db
}

func nativeExchangeAudit(t *testing.T, db *sql.DB, id string) *storage.NativeExchange {
	t.Helper()
	row := &storage.NativeExchange{ID: id}
	if err := db.QueryRowContext(t.Context(), `SELECT session_id,status,request_json,response_json,items_json FROM native_exchanges WHERE id=?`, id).
		Scan(&row.SessionID, &row.Status, &row.RequestJSON, &row.ResponseJSON, &row.ItemsJSON); err != nil {
		t.Fatal(err)
	}
	return row
}
