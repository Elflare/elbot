package sqlite

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/storage"
	toolstate "elbot/internal/tool/state"
)

const legacyToolSnapshots = `[{"name":"web_search","source":"native","description":"Search","schema":{"name":"web_search","description":"Search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}},"foreground_only":true},{"name":"lookup","canonical_name":"remote_lookup","source":"elwisp","description":"Remote lookup","schema":{"name":"remote_lookup","description":"Remote lookup","parameters":{"type":"object"}},"elwisp_name":"old-service","event_key":"event-1","endpoint":"https://example.invalid/tool","timeout_seconds":23}]`

func toolSnapshotMigrationDatabase(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tools.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version >= 27 {
			break
		}
		if err := applyMigration(t.Context(), db, m); err != nil {
			t.Fatal(err)
		}
	}
	return path, db
}

func TestToolSnapshotMigrationPreservesStateAndRunsOnce(t *testing.T) {
	path, db := toolSnapshotMigrationDatabase(t)
	metadata := `{"discovered_tools":["web_search","lookup","uncached"],"tool_cache":` + legacyToolSnapshots + `,"tool_tags":["web"],"elyph_rules_shown":true,"unknown":9007199254740993,"workspace_dir":"/work"}`
	if _, err := toolstate.DecodeState(metadata); err == nil || !strings.Contains(err.Error(), "discovered_tools") {
		t.Fatalf("legacy fixture did not reproduce the reported failure: %v", err)
	}
	insertOldOriginSession(t, db, "legacy", metadata)
	if _, err := db.Exec(`INSERT INTO messages(id,session_id,role,content,metadata,created_at) VALUES('message','legacy','user','keep history','{"custom":true}','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	store, err := New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	row, err := store.Sessions().Get(t.Context(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	state, err := toolstate.DecodeState(row.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	want, err := toolstate.DecodeState(`{"discovered_tools":` + legacyToolSnapshots + `,"tool_tags":["web"],"elyph_rules_shown":true}`)
	if err != nil || !reflect.DeepEqual(state, want) {
		t.Fatalf("tool snapshots changed: got %+v, want %+v, error %v", state, want, err)
	}
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["tool_cache"]; ok {
		t.Fatal("obsolete tool_cache survived")
	}
	if string(fields["unknown"]) != "9007199254740993" || string(fields["workspace_dir"]) != `"/work"` {
		t.Fatalf("unrelated metadata changed: %s", row.Metadata)
	}
	if string(fields["discovered_tools"]) != legacyToolSnapshots {
		t.Fatalf("cached JSON was not preserved: %s", fields["discovered_tools"])
	}
	var createdAt, updatedAt, content, messageMetadata string
	if err := db.QueryRow(`SELECT created_at,updated_at FROM sessions WHERE id='legacy'`).Scan(&createdAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if createdAt != "2026-01-01T00:00:00Z" || updatedAt != createdAt {
		t.Fatalf("session timestamps changed: %s, %s", createdAt, updatedAt)
	}
	if err := db.QueryRow(`SELECT content,metadata FROM messages WHERE id='message'`).Scan(&content, &messageMetadata); err != nil {
		t.Fatal(err)
	}
	if content != "keep history" || messageMetadata != `{"custom":true}` {
		t.Fatalf("history changed: %s, %s", content, messageMetadata)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// A post-migration legacy row is a sentinel: reopening must not rerun version 27.
	insertOldOriginSession(t, db, "after", metadata)
	reopened, err := New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var after string
	if err := db.QueryRow(`SELECT metadata FROM sessions WHERE id='after'`).Scan(&after); err != nil || after != metadata {
		t.Fatalf("migration ran again: %s, %v", after, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=27 AND name='migrate_discovered_tool_snapshots'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration not recorded exactly once: %d, %v", count, err)
	}
}

func TestToolSnapshotMigrationFormats(t *testing.T) {
	_, db := toolSnapshotMigrationDatabase(t)
	current := `[{"name":"current","source":"native","schema":{"name":"current","parameters":{"type":"object"}}}]`
	cases := []struct {
		name     string
		metadata any
		want     string
	}{
		{"names_only", `{"discovered_tools":["web_search"],"unknown":7}`, `{"unknown":7}`},
		{"null_cache", `{"discovered_tools":["web_search"],"tool_cache":null}`, `{}`},
		{"empty_cache", `{"discovered_tools":["web_search"],"tool_cache":[]}`, `{}`},
		{"cache_only", `{"tool_cache":` + legacyToolSnapshots + `}`, `{"discovered_tools":` + legacyToolSnapshots + `}`},
		{"null_names", `{"discovered_tools":null,"tool_cache":` + legacyToolSnapshots + `}`, `{"discovered_tools":` + legacyToolSnapshots + `}`},
		{"empty_names", `{"discovered_tools":[],"tool_cache":` + legacyToolSnapshots + `}`, `{"discovered_tools":` + legacyToolSnapshots + `}`},
		{"current", `{"discovered_tools":` + current + `}`, `{"discovered_tools":` + current + `}`},
		{"current_with_cache", `{"discovered_tools":` + current + `,"tool_cache":` + legacyToolSnapshots + `}`, `{"discovered_tools":` + current + `}`},
		{"empty_arrays", `{"discovered_tools":[],"tool_cache":[]}`, `{}`},
		{"null_fields", `{"discovered_tools":null,"tool_cache":null}`, `{}`},
		{"unrelated", `{ "unknown": 9007199254740993 }`, `{ "unknown": 9007199254740993 }`},
		{"empty_object", `{}`, `{}`},
		{"empty", "", ""},
		{"whitespace", "\t\r\n ", "\t\r\n "},
		{"sql_null", nil, ""},
	}
	for _, tc := range cases {
		insertOldOriginSession(t, db, tc.name, tc.metadata)
	}
	if err := runMigrations(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got sql.NullString
			if err := db.QueryRow(`SELECT metadata FROM sessions WHERE id=?`, tc.name).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got.String != tc.want || got.Valid != (tc.metadata != nil) {
				t.Fatalf("metadata = %+v, want %q", got, tc.want)
			}
			if _, err := toolstate.DecodeState(got.String); err != nil {
				t.Fatalf("migrated state cannot be decoded: %v", err)
			}
		})
	}
}

func TestToolSnapshotMigrationRollsBackInvalidMetadata(t *testing.T) {
	for _, invalid := range []string{
		`{broken`, `[]`, `null`, `"scalar"`,
		`{"discovered_tools":"invalid"}`, `{"discovered_tools":{}}`,
		`{"discovered_tools":["old",{"name":"new"}]}`,
		`{"discovered_tools":[23]}`, `{"discovered_tools":[null]}`,
		`{"tool_cache":"invalid"}`, `{"tool_cache":{}}`,
		`{"tool_cache":["old"]}`, `{"tool_cache":[null]}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			_, db := toolSnapshotMigrationDatabase(t)
			good := `{"discovered_tools":["web_search"],"tool_cache":` + legacyToolSnapshots + `}`
			insertOldOriginSession(t, db, "good", good)
			insertOldOriginSession(t, db, "bad", invalid)
			if err := runMigrations(t.Context(), db); err == nil {
				t.Fatal("invalid metadata silently migrated")
			}
			for id, want := range map[string]string{"good": good, "bad": invalid} {
				var got string
				if err := db.QueryRow(`SELECT metadata FROM sessions WHERE id=?`, id).Scan(&got); err != nil || got != want {
					t.Fatalf("migration partially committed %s: %s, %v", id, got, err)
				}
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=27`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed migration recorded: %d, %v", count, err)
			}
			if _, err := db.Exec(`UPDATE sessions SET metadata='{}' WHERE id='bad'`); err != nil {
				t.Fatal(err)
			}
			if err := runMigrations(t.Context(), db); err != nil {
				t.Fatalf("retry after repairing metadata: %v", err)
			}
			var metadata string
			if err := db.QueryRow(`SELECT metadata FROM sessions WHERE id='good'`).Scan(&metadata); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(metadata), &fields); err != nil || string(fields["discovered_tools"]) != legacyToolSnapshots {
				t.Fatalf("retry did not migrate tools: %s, %v", metadata, err)
			}
		})
	}
}
