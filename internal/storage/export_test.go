package storage

import (
	"database/sql"
	"testing"
)

func (s *Supervisor) DBForTest() *sql.DB   { return s.db }
func WithDBForTest(db *sql.DB) *Supervisor { return &Supervisor{db: db} }

var MigrateForTest = runMigrations

func MigrateBeforeForTest(t *testing.T, db *sql.DB, version int) {
	t.Helper()
	for _, m := range migrations {
		if m.version >= version {
			break
		}
		if err := applyMigration(t.Context(), db, m); err != nil {
			t.Fatal(err)
		}
	}
}
