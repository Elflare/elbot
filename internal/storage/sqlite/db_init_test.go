package sqlite

import (
	"database/sql"
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
