package module

import (
	"context"
	"testing"
	"time"

	"elbot/internal/security"
	"elbot/internal/storage/sqlite"
)

func TestModuleOwnsOneToolGraphAndDoesNotCloseStorage(t *testing.T) {
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m, err := NewModule(t.Context(), ModuleDeps{ConfigDir: t.TempDir(), Store: store, Policy: security.DefaultPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if m.Registry() != m.Registry() || m.Manager() != m.Manager() || m.Preloader() != m.Preloader() || m.State() != m.State() {
		t.Fatal("shared instances recreated")
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for range 2 {
		if err := m.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-m.Done():
	default:
		t.Fatal("loader survived shutdown")
	}
	if err := m.Start(); err == nil {
		t.Fatal("closed module restarted")
	}
	if err := store.Media().RecoverInterrupted(ctx); err != nil {
		t.Fatal("borrowed storage was closed", err)
	}
}
