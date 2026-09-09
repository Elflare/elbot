package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/media"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
)

func TestSandboxMediaCacheRefreshAndExpiry(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	item, err := center.ImportBytes(ctx, []byte("cached"), media.Input{Name: "input.txt"})
	if err != nil {
		t.Fatal(err)
	}
	sandbox := t.TempDir()
	bridge := &tool.MediaRuntime{Center: center, SandboxRoot: sandbox}
	call := bridge.NewCall()
	path, err := call.CachedExport(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -10)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := call.CachedExport(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().AddDate(0, 0, -7)
	if deleted, err := cleanupSandbox(ctx, sandbox, cutoff); err != nil || deleted != 0 {
		t.Fatalf("fresh cache deleted: %d %v", deleted, err)
	}
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if deleted, err := cleanupSandbox(ctx, sandbox, cutoff); err != nil || deleted != 1 {
		t.Fatalf("expired cache: %d %v", deleted, err)
	}
	if data, _, err := center.Read(ctx, item.ID); err != nil || string(data) != "cached" {
		t.Fatalf("media body deleted: %q %v", data, err)
	}
}
