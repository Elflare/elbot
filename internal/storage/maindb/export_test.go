package maindb

import (
	"database/sql"
	"testing"
)

func (s *Store) DBForTest() *sql.DB   { return s.db }
func WithDBForTest(db *sql.DB) *Store { return &Store{db: db} }

var MigrateForTest = runMigrations

func OpenBeforeForTest(t *testing.T, dsn string, version int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(t.Context(), migrationTableSQL); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version >= version {
			break
		}
		if err := applyMigration(t.Context(), db, m); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
