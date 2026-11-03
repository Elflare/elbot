package sqlite

import (
	"path/filepath"
	"testing"

	"elbot/internal/storage"
)

func TestRemoveUnusedIDsPreservesRecordsAndConstraints(t *testing.T) {
	db, err := openDB(t.Context(), filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version < 30 {
			if err := applyMigration(t.Context(), db, m); err != nil {
				t.Fatal(err)
			}
			if m.version == 28 {
				if _, err := db.Exec("UPDATE session_sequence SET value=77"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	sessions := &SessionRepository{db: db}
	if n, err := sessions.NextSequence(t.Context()); err != nil || n != 78 {
		t.Fatalf("migrated sequence=%d err=%v", n, err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(id,owner_id,platform,platform_scope_id,mode,status,created_at,updated_at) VALUES('legacy','cli:local','cli','local','work','active','now','now')`); err != nil {
		t.Fatal(err)
	}
	messages := &MessageRepository{db: db}
	if err := messages.Append(t.Context(), &storage.Message{ID: "message", SessionID: "legacy", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO platform_message_map VALUES('map-uuid','cli','local','platform-message','message','legacy','now');
 INSERT INTO tool_call_records VALUES('record-1','legacy','same-call','shell','cli:actor','low',1,NULL,'now','now','now');
 INSERT INTO tool_call_records VALUES('record-2','legacy','same-call','shell','cli:actor','low',0,'failed','now','now','now');`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"platform_message_map", "tool_call_records"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name='id'`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("table=%s id columns=%d err=%v", table, count, err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM tool_call_records WHERE tool_call_id='same-call'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("records=%d err=%v", count, err)
	}
	row, err := messages.FindByPlatformMessage(t.Context(), "cli", "private", "local", "platform-message")
	if err != nil || row.ID != "message" {
		t.Fatalf("mapping=%+v err=%v", row, err)
	}
	if err := messages.MapPlatformMessage(t.Context(), storage.PlatformMessageMap{Platform: "cli", ChatType: "private", ChatID: "local", PlatformMessageID: "platform-message", MessageID: "message", SessionID: "legacy"}); err == nil {
		t.Fatal("duplicate mapping accepted")
	}
	if err := sessions.Delete(t.Context(), "legacy"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"platform_message_map", "tool_call_records"} {
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cascade %s count=%d err=%v", table, count, err)
		}
	}
}
