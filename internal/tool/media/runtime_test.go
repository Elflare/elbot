package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/media"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
	"elbot/internal/tool/share"
	"elbot/internal/workspace"
)

func TestMediaCacheReuseRefreshAndRelease(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	center := mediatest.New(t, store, root)
	item, err := center.ImportBytes(ctx, []byte("hello"), media.Input{Name: "hello.txt"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewMediaRuntime(center, t.TempDir(), mediatest.Publisher(center))
	ctx = workspace.WithPathScope(ctx, workspace.PathScope{Dir: t.TempDir(), Restricted: true})
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
			call.Close()
			errors <- err
		}()
	}
	for range 8 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := center.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	call := runtime.NewCall()
	path, err := call.CachedExport(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(runtime.sandboxRoot, "media-inputs") {
		t.Fatalf("cache followed the invocation workspace: %s", path)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	call.Close()
	if _, err := center.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	call = runtime.NewCall()
	t.Cleanup(func() { call.Close() })
	again, err := call.CachedExport(workspace.WithPathScope(ctx, workspace.PathScope{Dir: t.TempDir()}), item.ID)
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
	call.Close()
	if _, err := center.Cleanup(ctx); err != nil {
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
	again, metadata, err := private.Export(ctx, item.ID, base)
	if err != nil || again != relative || metadata.Name != item.Name || metadata.Size != item.Size {
		t.Fatalf("repeated export %q %#v %v", again, metadata, err)
	}
	if data, err := os.ReadFile(filepath.Join(base, relative)); err != nil || string(data) != "hello" {
		t.Fatalf("private content %q %v", data, err)
	}
	output := filepath.Join(filepath.Dir(relative), "output.txt")
	if err := os.WriteFile(filepath.Join(base, output), []byte("output"), 0600); err != nil {
		t.Fatal(err)
	}
	imported, err := private.Import(ctx, base, output, share.MediaImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs, err = store.MediaReferences().ListMediaIDs(ctx, imported.ID)
	if err != nil || len(refs) == 0 || refs[0].OwnerType != "request" {
		t.Fatalf("output is not held by the invocation: %v %v", refs, err)
	}
	private.Close()
	if _, err := center.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	refs, err = store.MediaReferences().ListMediaIDs(ctx, imported.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("import references survived close: %v %v", refs, err)
	}
	if _, err := os.Stat(filepath.Join(base, relative)); !os.IsNotExist(err) {
		t.Fatalf("private export survived: %v", err)
	}
	for _, path := range []string{"../escape", "/absolute", `C:\escape`, `..\escape`} {
		if _, err := runtime.NewCall().Import(ctx, base, path, share.MediaImportOptions{}); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "link.txt")); err == nil {
		if _, err := runtime.NewCall().Import(ctx, base, "link.txt", share.MediaImportOptions{}); err == nil {
			t.Fatal("accepted escaped symlink")
		}
	}
}

func TestMediaExportFailureCanBeRetriedAndCanceled(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared=%t", shared), func(t *testing.T) {
			ctx := t.Context()
			store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			root := t.TempDir()
			center := mediatest.New(t, store, root)
			item, err := center.ImportBytes(ctx, []byte("complete content"), media.Input{Name: "input.txt"}, media.ImportLimits{})
			if err != nil {
				t.Fatal(err)
			}
			runtime := NewMediaRuntime(center, t.TempDir(), mediatest.Publisher(center))
			call := runtime.NewCall()
			defer call.Close()
			base := t.TempDir()
			workspace, err := call.Workspace(base)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(base, workspace, mediaFilename(mediaInfo(item)))
			export := func(ctx context.Context) (string, error) {
				path, _, err := call.Export(ctx, item.ID, base)
				return filepath.Join(base, path), err
			}
			if shared {
				destination = filepath.Join(runtime.sandboxRoot, "media-inputs", mediaFilename(mediaInfo(item)))
				export = func(ctx context.Context) (string, error) { return call.CachedExport(ctx, item.ID) }
			}
			if err := os.MkdirAll(destination, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := export(ctx); err == nil {
				t.Fatal("accepted directory as export destination")
			}
			if temporary, err := filepath.Glob(destination + ".*.tmp"); err != nil || len(temporary) != 0 {
				t.Fatalf("failed export left temporary files: %v %v", temporary, err)
			}
			if err := os.Remove(destination); err != nil {
				t.Fatal(err)
			}
			path, err := export(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "complete content" {
				t.Fatalf("retried export %q %v", data, err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := export(canceled); !errors.Is(err, context.Canceled) {
				t.Fatalf("prepared export ignored cancellation: %v", err)
			}
			call.Close()
			if _, err := center.Cleanup(ctx); err != nil {
				t.Fatal(err)
			}
			refs, err := store.MediaReferences().ListMediaIDs(ctx, item.ID)
			if err != nil || len(refs) != 0 {
				t.Fatalf("export references survived close: %v %v", refs, err)
			}
		})
	}
}
