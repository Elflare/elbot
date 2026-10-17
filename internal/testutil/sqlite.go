// Package testutil provides fixtures shared by repository tests.
package testutil

import (
	"testing"

	"elbot/internal/storage/sqlite"
)

// NewSQLiteStore creates an independent memory database with the current schema.
// Tests of persistence across reopening should use sqlite.New with a file path.
func NewSQLiteStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("create SQLite test database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
