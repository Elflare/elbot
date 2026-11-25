package storage

import (
	"context"
	"database/sql"
	"testing"
)

func OpenMainForTest(ctx context.Context, path string) (*Supervisor, error) {
	return NewSupervisor(ctx, path, ":memory:")
}
func openHistoryForTest(ctx context.Context, path string) (*Supervisor, error) {
	return NewSupervisor(ctx, ":memory:", path)
}
func NewStoreForTest(t *testing.T) *Supervisor { t.Helper(); return newTestStore(t) }
func (s *Supervisor) DBForTest() *sql.DB       { return s.db }
func WithDBForTest(db *sql.DB) *Supervisor     { return &Supervisor{db: db} }

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
