package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type trackingDeleteBackend struct {
	Backend
	name  string
	calls *[]string
	fail  bool
}

func (b *trackingDeleteBackend) Remove(ctx context.Context, item *storage.Media) error {
	*b.calls = append(*b.calls, b.name)
	if b.fail {
		return fmt.Errorf("temporary %s backend error", b.name)
	}
	if b.Backend == nil {
		return nil
	}
	return b.Backend.Remove(ctx, item)
}

func TestCleanupUsesStoredLocationsAcrossBackendChanges(t *testing.T) {
	t.Run("s3 to hybrid removes remote object", func(t *testing.T) {
		ctx := context.Background()
		store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		calls := []string{}
		root := t.TempDir()
		local := &trackingDeleteBackend{Backend: &LocalBackend{Root: root}, name: "local", calls: &calls}
		remote := &trackingDeleteBackend{name: "remote", calls: &calls}
		m := NewManager(store, root, local)
		m.Remote = remote
		item := &storage.Media{
			ID:        IDPrefix + strings.Repeat("a", 64),
			Name:      "remote.bin",
			MIMEType:  "application/octet-stream",
			Size:      1,
			Backend:   "s3",
			ObjectKey: "media/remote",
		}
		if err := store.Media().Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
		m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
		if err := m.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(calls, []string{"remote"}) {
			t.Fatalf("remove calls = %v", calls)
		}
		if _, err := store.Media().Get(ctx, item.ID); err != storage.ErrNotFound {
			t.Fatalf("record survives: %v", err)
		}
	})

	t.Run("hybrid to s3 retries remote before local", func(t *testing.T) {
		ctx := context.Background()
		store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		calls := []string{}
		root := t.TempDir()
		local := &trackingDeleteBackend{Backend: &LocalBackend{Root: root}, name: "local", calls: &calls}
		remote := &trackingDeleteBackend{name: "remote", calls: &calls, fail: true}
		m := NewManager(store, root, local)
		item, err := m.ImportBytes(ctx, []byte("dual"), Input{Name: "dual.bin"})
		if err != nil {
			t.Fatal(err)
		}
		item.ObjectKey = "media/dual"
		if err := store.Media().Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
		m.Backend = remote
		m.Remote = remote
		m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
		if err := m.Cleanup(ctx); err == nil {
			t.Fatal("expected remote delete failure")
		}
		if !reflect.DeepEqual(calls, []string{"remote"}) {
			t.Fatalf("remove calls before retry = %v", calls)
		}
		if _, err := os.Stat(item.LocalPath); err != nil {
			t.Fatalf("local copy removed before remote: %v", err)
		}
		marked, err := store.Media().Get(ctx, item.ID)
		if err != nil || !marked.Deleting {
			t.Fatalf("retry metadata = %#v, %v", marked, err)
		}

		remote.fail = false
		if err := m.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(calls, []string{"remote", "remote", "local"}) {
			t.Fatalf("remove calls after retry = %v", calls)
		}
		if _, err := os.Stat(item.LocalPath); !os.IsNotExist(err) {
			t.Fatalf("local copy survives: %v", err)
		}
		if _, err := store.Media().Get(ctx, item.ID); err != storage.ErrNotFound {
			t.Fatalf("record survives: %v", err)
		}
	})

	t.Run("local failure after remote deletion retries idempotently", func(t *testing.T) {
		ctx := context.Background()
		store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		calls := []string{}
		root := t.TempDir()
		local := &trackingDeleteBackend{Backend: &LocalBackend{Root: root}, name: "local", calls: &calls, fail: true}
		remote := &trackingDeleteBackend{name: "remote", calls: &calls}
		m := NewManager(store, root, local)
		m.Remote = remote
		item, err := m.ImportBytes(ctx, []byte("partial delete"), Input{Name: "partial.bin"})
		if err != nil {
			t.Fatal(err)
		}
		item.ObjectKey = "media/partial"
		if err := store.Media().Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
		m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
		if err := m.Cleanup(ctx); err == nil {
			t.Fatal("expected local delete failure")
		}
		if !reflect.DeepEqual(calls, []string{"remote", "local"}) {
			t.Fatalf("first remove calls = %v", calls)
		}
		if _, err := os.Stat(item.LocalPath); err != nil {
			t.Fatalf("local copy missing after failed deletion: %v", err)
		}
		if marked, err := store.Media().Get(ctx, item.ID); err != nil || !marked.Deleting {
			t.Fatalf("retry metadata = %#v, %v", marked, err)
		}

		local.fail = false
		if err := m.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(calls, []string{"remote", "local", "remote", "local"}) {
			t.Fatalf("retry remove calls = %v", calls)
		}
		if _, err := store.Media().Get(ctx, item.ID); err != storage.ErrNotFound {
			t.Fatalf("record survives: %v", err)
		}
	})

	t.Run("missing remote preserves copies and retry metadata", func(t *testing.T) {
		ctx := context.Background()
		store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		calls := []string{}
		root := t.TempDir()
		local := &trackingDeleteBackend{Backend: &LocalBackend{Root: root}, name: "local", calls: &calls}
		m := NewManager(store, root, local)
		item, err := m.ImportBytes(ctx, []byte("dual without remote"), Input{Name: "dual.bin"})
		if err != nil {
			t.Fatal(err)
		}
		item.ObjectKey = "media/dual"
		if err := store.Media().Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
		m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
		if err := m.Cleanup(ctx); err == nil {
			t.Fatal("expected unavailable remote backend")
		}
		if len(calls) != 0 {
			t.Fatalf("removed physical copies without remote backend: %v", calls)
		}
		if _, err := os.Stat(item.LocalPath); err != nil {
			t.Fatalf("local copy missing: %v", err)
		}
		marked, err := store.Media().Get(ctx, item.ID)
		if err != nil || !marked.Deleting {
			t.Fatalf("retry metadata = %#v, %v", marked, err)
		}
	})
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
