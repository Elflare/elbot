package commands

import (
	"context"
	"testing"

	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func newCommandTestStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
