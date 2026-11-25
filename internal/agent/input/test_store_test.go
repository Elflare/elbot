package input

import (
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"testing"
)

func newTestStore(t *testing.T) *storage.Supervisor {
	t.Helper()
	s, err := testutil.OpenStore(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
