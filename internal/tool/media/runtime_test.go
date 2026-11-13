package media

import (
	"context"
	"elbot/internal/operationresult"
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
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, root)
	item, err := center.ImportBytes(ctx, []byte("hello"), media.Input{Name: "hello.txt"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewMediaRuntime(center, t.TempDir(), center.PublishUsageFinished)
	ctx = workspace.WithPathScope(ctx, workspace.PathScope{Dir: t.TempDir(), Restricted: true})
	errors := make(chan error, 8)
	for range 8 {
		go func() {
			call := runtime.NewCall()
			path, err := call.CachedExport(ctx, item.Data.ID)
			if err == nil && path.Status != operationresult.Success {
				t.Errorf("export=%+v", path)
			}
			if err == nil && path.Status == operationresult.Success {
				data, readErr := os.ReadFile(path.Data)
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
	if err := center.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	call := runtime.NewCall()
	path, err := call.CachedExport(ctx, item.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path.Data) != filepath.Join(runtime.sandboxRoot, "media-inputs") {
		t.Fatalf("cache followed the invocation workspace: %s", path)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(path.Data, old, old); err != nil {
		t.Fatal(err)
	}
	call.Close()
	if err := center.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	call = runtime.NewCall()
	t.Cleanup(func() { call.Close() })
	again, err := call.CachedExport(workspace.WithPathScope(ctx, workspace.PathScope{Dir: t.TempDir()}), item.Data.ID)
	if err != nil || again.Status != operationresult.Success || again.Data != path.Data {
		t.Fatalf("cache path %s: %v", again, err)
	}
	info, err := os.Stat(path.Data)
	if err != nil || info.ModTime().Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("cache not touched: %v %v", info, err)
	}
	refs, err := store.MediaReferences().ListMediaIDs(ctx, item.Data.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs %v %v", refs, err)
	}
	call.Close()
	if err := center.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	refs, err = store.MediaReferences().ListMediaIDs(ctx, item.Data.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("refs after close %v %v", refs, err)
	}
	if data, err := os.ReadFile(path.Data); err != nil || string(data) != "hello" {
		t.Fatalf("cache removed: %q %v", data, err)
	}
	private := runtime.NewCall()
	base := t.TempDir()
	relative, _, err := private.Export(ctx, item.Data.ID, base)
	if err != nil || filepath.IsAbs(relative) {
		t.Fatalf("export %q %v", relative, err)
	}
	relativeAgain, metadata, err := private.Export(ctx, item.Data.ID, base)
	if err != nil || relativeAgain != relative || metadata.Status != operationresult.Success || metadata.Data.Name != item.Data.Name || metadata.Data.Size != item.Data.Size {
		t.Fatalf("repeated export %q %#v %v", relativeAgain, metadata, err)
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
	refs, err = store.MediaReferences().ListMediaIDs(ctx, imported.Data.ID)
	if err != nil || len(refs) == 0 || refs[0].OwnerType != "request" {
		t.Fatalf("output is not held by the invocation: %v %v", refs, err)
	}
	private.Close()
	if err := center.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	refs, err = store.MediaReferences().ListMediaIDs(ctx, imported.Data.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("import references survived close: %v %v", refs, err)
	}
	if _, err := os.Stat(filepath.Join(base, relative)); !os.IsNotExist(err) {
		t.Fatalf("private export survived: %v", err)
	}
	for _, path := range []string{"../escape", "/absolute", `C:\escape`, `..\escape`} {
		if result, err := runtime.NewCall().Import(ctx, base, path, share.MediaImportOptions{}); err != nil || result.Status == operationresult.Success {
			t.Fatalf("accepted %s", path)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "link.txt")); err == nil {
		if result, err := runtime.NewCall().Import(ctx, base, "link.txt", share.MediaImportOptions{}); err != nil || result.Status == operationresult.Success {
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
			center := mediatest.New(t, media.MediaCenterDeps{Store: store}, root)
			item, err := center.ImportBytes(ctx, []byte("complete content"), media.Input{Name: "input.txt"}, media.ImportLimits{})
			if err != nil {
				t.Fatal(err)
			}
			runtime := NewMediaRuntime(center, t.TempDir(), center.PublishUsageFinished)
			call := runtime.NewCall()
			defer call.Close()
			base := t.TempDir()
			workspace, err := call.Workspace(base)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(base, workspace, mediaFilename(mediaInfo(item.Data)))
			export := func(ctx context.Context) (operationresult.Result[string], error) {
				path, result, err := call.Export(ctx, item.Data.ID, base)
				if err != nil {
					return operationresult.Result[string]{}, err
				}
				if result.Status != operationresult.Success {
					return operationresult.Result[string]{Status: result.Status, Message: result.Message}, nil
				}
				return operationresult.Result[string]{Status: operationresult.Success, Data: filepath.Join(base, path)}, nil
			}
			if shared {
				destination = filepath.Join(runtime.sandboxRoot, "media-inputs", mediaFilename(mediaInfo(item.Data)))
				export = func(ctx context.Context) (operationresult.Result[string], error) {
					return call.CachedExport(ctx, item.Data.ID)
				}
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
			if data, err := os.ReadFile(path.Data); err != nil || string(data) != "complete content" {
				t.Fatalf("retried export %q %v", data, err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := export(canceled); !errors.Is(err, context.Canceled) {
				t.Fatalf("prepared export ignored cancellation: %v", err)
			}
			call.Close()
			if err := center.Cleanup(ctx); err != nil {
				t.Fatal(err)
			}
			refs, err := store.MediaReferences().ListMediaIDs(ctx, item.Data.ID)
			if err != nil || len(refs) != 0 {
				t.Fatalf("export references survived close: %v %v", refs, err)
			}
		})
	}
}
