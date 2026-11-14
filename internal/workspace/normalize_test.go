package workspace

import (
	"context"
	"elbot/internal/operationresult"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRestrictedPathKeepsOrdinaryNames(t *testing.T) {
	for raw, want := range map[string]string{
		"report..txt":        "report..txt",
		"nested\\file.txt":   filepath.Join("nested", "file.txt"),
		"./nested//file.txt": filepath.Join("nested", "file.txt"),
		".":                  ".",
	} {
		got, err := NormalizeRestrictedPath(raw)
		if err != nil || got != want {
			t.Fatalf("%q: got=%q want=%q err=%v", raw, got, want, err)
		}
	}
}

func TestResolveWorkspacePathRequiresRegularFile(t *testing.T) {
	dir := t.TempDir()
	ctx := WithPathScope(context.Background(), PathScope{Dir: dir, Restricted: true})
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := PathResolveOptions{RequireRegularFile: true, AllowDirectory: true}
	if result, err := ResolveWorkspacePath(ctx, "file", opts); err != nil || result.Status != operationresult.Success {
		t.Fatalf("regular file: %+v %v", result, err)
	}
	for _, path := range []string{".", "missing"} {
		if result, err := ResolveWorkspacePath(ctx, path, opts); err != nil || result.Status == operationresult.Success || result.Message == "" {
			t.Fatalf("regular file %s: %+v %v", path, result, err)
		}
	}
	if info, err := os.Stat(os.DevNull); err == nil && !info.Mode().IsRegular() {
		if result, err := validateResolvedPath(os.DevNull, opts); err != nil || result.Status != operationresult.InvalidTarget {
			t.Fatalf("device: %+v %v", result, err)
		}
	}
}
