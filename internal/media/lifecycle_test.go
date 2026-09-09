package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type failingDeleteBackend struct {
	Backend
	fail bool
}

func (b *failingDeleteBackend) Remove(ctx context.Context, item *storage.Media) error {
	if b.fail {
		return fmt.Errorf("temporary backend error")
	}
	return b.Backend.Remove(ctx, item)
}
func TestCleanupRetriesBackendFailure(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	backend := &failingDeleteBackend{Backend: &LocalBackend{Root: root}, fail: true}
	m := NewManager(store, root, backend)
	item, err := m.ImportBytes(ctx, []byte("retry"), Input{Name: "retry.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// Exercise a non-local backend through the same interface without a live S3 service.
	item.Backend = "s3"
	if err := store.Media().Upsert(ctx, item); err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	if err := m.Cleanup(ctx); err == nil {
		t.Fatal("expected backend failure")
	}
	marked, err := store.Media().Get(ctx, item.ID)
	if err != nil || !marked.Deleting {
		t.Fatalf("lost retry metadata %v %v", marked, err)
	}
	if _, err := os.Stat(item.LocalPath); err != nil {
		t.Fatal(err)
	}
	backend.fail = false
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Media().Get(ctx, item.ID); err != storage.ErrNotFound {
		t.Fatalf("record survives %v", err)
	}
	if _, err := os.Stat(item.LocalPath); !os.IsNotExist(err) {
		t.Fatalf("body survives %v", err)
	}
}

func TestOpenProtectsMediaUntilReaderCloses(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})
	item, err := m.ImportBytes(ctx, []byte("reading"), Input{})
	if err != nil {
		t.Fatal(err)
	}
	reader, _, err := m.Open(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Media().Get(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Media().Get(ctx, item.ID); err != storage.ErrNotFound {
		t.Fatal(err)
	}
}
