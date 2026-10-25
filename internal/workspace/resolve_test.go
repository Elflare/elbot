package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/contextinfo"
)

func TestResolveWorkspacePathUsesScopeAndKeepsSessionSelection(t *testing.T) {
	sessionDir, taskDir := t.TempDir(), t.TempDir()
	for _, dir := range []string{sessionDir, taskDir} {
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(dir), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store := &fallbackWorkspaceStore{dir: sessionDir}
	ctx := WithWorkspaceStore(t.Context(), store)
	// A restricted path scope does not require or imply a background source.
	scoped := WithPathScope(ctx, PathScope{Dir: taskDir, Restricted: true})
	result, err := ResolveWorkspacePath(scoped, "file.txt", PathResolveOptions{})
	if err != nil || result.Path != filepath.Join(taskDir, "file.txt") || result.BaseDir != taskDir || result.WasAbs {
		t.Fatalf("scoped result=%+v err=%v", result, err)
	}
	if contextinfo.BackgroundContext(scoped) || store.dir != sessionDir {
		t.Fatal("path scope changed execution identity or session selection")
	}
	result, err = ResolveWorkspacePath(WithoutPathScope(scoped), "file.txt", PathResolveOptions{})
	if err != nil || result.Path != filepath.Join(sessionDir, "file.txt") {
		t.Fatalf("restored result=%+v err=%v", result, err)
	}
	// Source identity alone must not install a path restriction.
	result, err = ResolveWorkspacePath(contextinfo.WithBackgroundKind(ctx, contextinfo.BackgroundKindCron), filepath.Join(taskDir, "file.txt"), PathResolveOptions{})
	if err != nil || !result.WasAbs {
		t.Fatalf("absolute result=%+v err=%v", result, err)
	}
}

func TestResolveWorkspacePathChecksTargetAndCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx := WithPathScope(t.Context(), PathScope{Dir: dir, Restricted: true})
	if _, err := ResolveWorkspacePath(ctx, "missing", PathResolveOptions{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file accepted: %v", err)
	}
	if _, err := ResolveWorkspacePath(ctx, "missing", PathResolveOptions{AllowCreate: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWorkspacePath(ctx, ".", PathResolveOptions{}); err == nil {
		t.Fatal("directory accepted as a file")
	}
	if _, err := ResolveWorkspacePath(ctx, ".", PathResolveOptions{AllowDirectory: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ResolveWorkspacePath(ctx, "missing", PathResolveOptions{AllowCreate: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestCurrentWorkspaceDirNormalizesRelativeDirectories(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, ctx := range []context.Context{
		WithPathScope(t.Context(), PathScope{Dir: "project"}),
		WithWorkspaceStore(t.Context(), &fallbackWorkspaceStore{dir: "project"}),
	} {
		if got, err := CurrentWorkspaceDir(ctx); err != nil || got != dir {
			t.Fatalf("relative directory=%q err=%v", got, err)
		}
	}
}
