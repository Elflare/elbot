package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxDirComputesDirectoryWithoutCreatingIt(t *testing.T) {
	root := t.TempDir()
	dir, err := SandboxDir(root, "cron/task")
	if err != nil || dir != filepath.Join(root, "cron", "task") {
		t.Fatalf("task directory=%q err=%v", dir, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path computation created a directory: %v", err)
	}
	if got, err := SandboxDir(root, "."); err != nil || got != root {
		t.Fatalf("root directory=%q err=%v", got, err)
	}
	if _, err := SandboxDir("", "cron/task"); err == nil {
		t.Fatal("empty sandbox root accepted")
	}
}

func TestRestrictedPathRejectsOutsideAndAbsoluteInputs(t *testing.T) {
	dir := t.TempDir()
	scope := PathScope{Dir: dir, Restricted: true}
	for _, path := range []string{"", "../outside", "a/../../outside", "a/../b", "a/..", `a\..\b`, `..\outside`, "/outside", `C:\outside`, "C:relative", "file:stream", `\\server\share`, "~/outside"} {
		t.Run(path, func(t *testing.T) {
			if _, err := ResolveSandboxRelativePath(scope, path); err == nil {
				t.Fatalf("accepted restricted path %q", path)
			}
			ctx := WithPathScope(t.Context(), scope)
			if _, err := ResolveWorkspacePath(ctx, path, PathResolveOptions{AllowCreate: true}); err == nil {
				t.Fatalf("unified resolver accepted restricted path %q", path)
			}
		})
	}
	if _, err := ResolveSandboxRelativePath(PathScope{}, "file"); err == nil {
		t.Fatal("missing scope directory accepted")
	}
	if got, err := ResolveSandboxRelativePath(scope, `nested\file`); err != nil || got != filepath.Join(dir, "nested", "file") {
		t.Fatalf("relative result=%q err=%v", got, err)
	}
}
