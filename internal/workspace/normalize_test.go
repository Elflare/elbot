package workspace

import (
	"context"
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
	if _, err := ResolveWorkspacePath(ctx, "file", opts); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".", "missing"} {
		if _, err := ResolveWorkspacePath(ctx, path, opts); err == nil {
			t.Fatalf("accepted %s as regular file", path)
		}
	}
	if info, err := os.Stat(os.DevNull); err == nil && !info.Mode().IsRegular() {
		if err := validateResolvedPath(os.DevNull, opts); err == nil {
			t.Fatal("accepted a device as regular file")
		}
	}
}
