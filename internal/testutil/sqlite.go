// Package testutil provides fixtures shared by repository tests.
package testutil

import (
	"context"
	"elbot/internal/storage"
	"testing"
)

func OpenStore(ctx context.Context, path string) (*storage.Supervisor, error) {
	return storage.NewSupervisor(ctx, path, ":memory:")
}
func OpenChatHistory(ctx context.Context, path string) (*storage.Supervisor, error) {
	return storage.NewSupervisor(ctx, ":memory:", path)
}
func NewSQLiteStore(t *testing.T) *storage.Supervisor {
	t.Helper()
	s, e := OpenStore(t.Context(), ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
