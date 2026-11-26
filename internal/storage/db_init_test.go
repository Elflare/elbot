package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNewDatabaseMatchesFullyMigratedDatabase(t *testing.T) {
	created, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = created.Close() })
	created.SetMaxOpenConns(1)
	if _, err := created.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	// Compare the snapshot itself so missing definitions or baseline versions
	// cannot be hidden by initializeDB applying the remaining migrations.
	if _, err := created.ExecContext(t.Context(), createLatestDBSQL); err != nil {
		t.Fatal(err)
	}
	migrated, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	migrated.SetMaxOpenConns(1)
	if _, err := migrated.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(t.Context(), migrated); err != nil {
		t.Fatal(err)
	}

	readRows := func(db *sql.DB, query string, args ...any) [][]any {
		t.Helper()
		rows, err := db.QueryContext(t.Context(), query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var result [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			result = append(result, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	compare := func(query string, args ...any) [][]any {
		t.Helper()
		got, want := readRows(created, query, args...), readRows(migrated, query, args...)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("database structure differs for %s %v:\nnew: %v\nmigrated: %v", query, args, got, want)
		}
		return got
	}

	compare(`SELECT version, name FROM schema_migrations ORDER BY version`)
	objects := compare(`SELECT type, name, tbl_name FROM sqlite_master WHERE name NOT GLOB 'sqlite_*' ORDER BY type, name`)
	for _, object := range objects {
		kind, name := object[0].(string), object[1].(string)
		switch kind {
		case "table":
			compare(`SELECT * FROM pragma_table_info(?) ORDER BY cid`, name)
			compare(`SELECT * FROM pragma_foreign_key_list(?) ORDER BY id, seq`, name)
			indexes := compare(`SELECT name, "unique", origin, partial FROM pragma_index_list(?) ORDER BY name`, name)
			for _, index := range indexes {
				compare(`SELECT * FROM pragma_index_xinfo(?) ORDER BY seqno`, index[0])
			}
		default:
			var got, want string
			if err := created.QueryRow(`SELECT sql FROM sqlite_master WHERE name = ?`, name).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if err := migrated.QueryRow(`SELECT sql FROM sqlite_master WHERE name = ?`, name).Scan(&want); err != nil {
				t.Fatal(err)
			}
			if strings.Join(strings.Fields(got), " ") != strings.Join(strings.Fields(want), " ") {
				t.Errorf("%s %s differs:\nnew: %s\nmigrated: %s", kind, name, got, want)
			}
		}
	}
}

func TestMessageSegmentsMigrationMovesUserSegmentsAndPreservesMetadata(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create migration table: %v", err)
	}
	for _, migration := range migrations[:10] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.version, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sessions (id, owner_id, platform, platform_scope_id, mode, status, created_at, updated_at) VALUES ('s1', 'u1', 'cli', 'local', 'work', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	legacyMetadata := `{"segments":[{"type":"text","text":"看图"},{"type":"image","url":"https://example.com/a.png"}],"custom":true}`
	if _, err := db.ExecContext(ctx, `INSERT INTO messages (id, session_id, role, content, metadata, created_at) VALUES ('m1', 's1', 'user', '看图', ?, '2026-01-01T00:00:00Z')`, legacyMetadata); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	if err := applyMigration(ctx, db, migrations[10]); err != nil {
		t.Fatalf("apply segments migration: %v", err)
	}
	var segments, metadata string
	if err := db.QueryRowContext(ctx, `SELECT segments, metadata FROM messages WHERE id = 'm1'`).Scan(&segments, &metadata); err != nil {
		t.Fatalf("query migrated message: %v", err)
	}
	if !strings.Contains(segments, `"type":"image"`) || metadata != `{"custom":true}` {
		t.Fatalf("segments = %q, metadata = %q", segments, metadata)
	}
}

func TestStructuredMessageMigrationMovesTranscriptFieldsAndPreservesInternalMetadata(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "structured-messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:31] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.version, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sessions (id, owner_platform, owner_id, platform, chat_type, chat_id, mode, status, created_at, updated_at) VALUES ('s1', 'cli', 'u1', 'cli', 'private', 'local', 'work', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	assistantMetadata := `{"raw_text":"model source","tool_calls":[{"ID":"call-1","Name":"shell","Arguments":"{}"}],"tool_batch_status":"completed","tool_result_message_ids":["t1"],"elyph_rules_shown":true,"custom":"keep"}`
	toolMetadata := `{"name":"shell","tool_result_status":"completed","custom":"also keep"}`
	for _, row := range []struct{ id, role, metadata string }{{"a1", "assistant", assistantMetadata}, {"t1", "tool", toolMetadata}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO messages (id, session_id, role, content, metadata, created_at) VALUES (?, 's1', ?, '', ?, '2026-01-01T00:00:00Z')`, row.id, row.role, row.metadata); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyMigration(ctx, db, migrations[31]); err != nil {
		t.Fatalf("apply structured message migration: %v", err)
	}
	var source, callsJSON, batchStatus, resultStatus, toolName, assistantMeta, toolMeta string
	if err := db.QueryRowContext(ctx, `SELECT source_content, tool_calls, tool_batch_status, metadata FROM messages WHERE id='a1'`).Scan(&source, &callsJSON, &batchStatus, &assistantMeta); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT tool_name, tool_result_status, metadata FROM messages WHERE id='t1'`).Scan(&toolName, &resultStatus, &toolMeta); err != nil {
		t.Fatal(err)
	}
	var calls []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(callsJSON), &calls); err != nil {
		t.Fatal(err)
	}
	if source != "model source" || batchStatus != "completed" || len(calls) != 1 || calls[0].ID != "call-1" || calls[0].Name != "shell" || toolName != "shell" || resultStatus != "completed" {
		t.Fatalf("migrated fields: source=%q calls=%s batch=%q tool=%q result=%q", source, callsJSON, batchStatus, toolName, resultStatus)
	}
	if !strings.Contains(assistantMeta, `"elyph_rules_shown":true`) || !strings.Contains(assistantMeta, `"custom":"keep"`) || strings.Contains(assistantMeta, `"tool_calls"`) || !strings.Contains(assistantMeta, `"tool_result_message_ids"`) {
		t.Fatalf("assistant metadata = %s", assistantMeta)
	}
	if !strings.Contains(toolMeta, `"custom":"also keep"`) || strings.Contains(toolMeta, `"tool_result_status"`) || strings.Contains(toolMeta, `"name"`) {
		t.Fatalf("tool metadata = %s", toolMeta)
	}
}
