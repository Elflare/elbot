package input

import (
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"testing"
)

func newTestStore(t *testing.T) storage.Store {
	t.Helper()
	s, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
