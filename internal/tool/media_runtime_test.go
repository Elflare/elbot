package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/media"
	"elbot/internal/storage/sqlite"
)

func TestMediaCacheReuseRefreshAndRelease(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	item, err := center.ImportBytes(ctx, []byte("hello"), media.Input{Name: "hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &MediaRuntime{Center: center, SandboxRoot: t.TempDir()}
	errors := make(chan error, 8)
	for range 8 {
		go func() {
			call := runtime.NewCall()
			path, err := call.CachedExport(ctx, item.ID)
			if err == nil {
				data, readErr := os.ReadFile(path)
				err = readErr
				if err == nil && string(data) != "hello" {
					err = fmt.Errorf("incomplete cache: %q", data)
				}
			}
			closeErr := call.Close()
			if err == nil {
				err = closeErr
			}
			errors <- err
		}()
	}
	for range 8 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	call := runtime.NewCall()
	path, err := call.CachedExport(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	again, err := call.CachedExport(ctx, item.ID)
	if err != nil || again != path {
		t.Fatalf("cache path %s: %v", again, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.ModTime().Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("cache not touched: %v %v", info, err)
	}
	refs, err := store.MediaReferences().ListMediaIDs(ctx, item.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs %v %v", refs, err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	refs, err = store.MediaReferences().ListMediaIDs(ctx, item.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("refs after close %v %v", refs, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "hello" {
		t.Fatalf("cache removed: %q %v", data, err)
	}
	private := runtime.NewCall()
	base := t.TempDir()
	relative, _, err := private.Export(ctx, item.ID, base)
	if err != nil || filepath.IsAbs(relative) {
		t.Fatalf("export %q %v", relative, err)
	}
	if err := private.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, relative)); !os.IsNotExist(err) {
		t.Fatalf("private export survived: %v", err)
	}
	for _, path := range []string{"../escape", "/absolute", `C:\escape`, `..\escape`} {
		if _, err := runtime.NewCall().Import(ctx, base, path, media.Input{}); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "link.txt")); err == nil {
		if _, err := runtime.NewCall().Import(ctx, base, "link.txt", media.Input{}); err == nil {
			t.Fatal("accepted escaped symlink")
		}
	}
}

func TestMediaInputRejectsInvalidAndExtraFields(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"media_id":"media:` + strings.Repeat("a", 64) + `"}`, `{"media":"bad"}`, `{"media":"media:` + strings.Repeat("a", 64) + `","path":"escape"}`} {
		var input MediaInput
		if err := json.Unmarshal([]byte(raw), &input); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
