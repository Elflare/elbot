package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fallbackWorkspaceStore struct {
	dir         string
	readErr     error
	clearErr    error
	beforeClear func()
	clearCalls  int
}

func (s *fallbackWorkspaceStore) GetWorkspaceDir(context.Context) (string, error) {
	return s.dir, s.readErr
}
func (s *fallbackWorkspaceStore) SetWorkspaceDir(_ context.Context, dir string) error {
	s.dir = dir
	return nil
}
func (s *fallbackWorkspaceStore) ClearWorkspaceDir(context.Context) error {
	s.dir = ""
	return nil
}
func (s *fallbackWorkspaceStore) ClearWorkspaceDirIfMatches(_ context.Context, dir string) (bool, error) {
	s.clearCalls++
	if s.beforeClear != nil {
		s.beforeClear()
		s.beforeClear = nil
	}
	if s.clearErr != nil {
		return false, s.clearErr
	}
	if s.dir != dir {
		return false, nil
	}
	s.dir = ""
	return true, nil
}

func TestCurrentWorkspaceDirUsesPathScopeWithoutReadingStore(t *testing.T) {
	store := &fallbackWorkspaceStore{readErr: errors.New("session must not be read")}
	dir := filepath.Join(t.TempDir(), "not-created")
	for _, restricted := range []bool{false, true} {
		ctx := WithPathScope(WithWorkspaceStore(t.Context(), store), PathScope{Dir: dir, Restricted: restricted})
		if got, err := CurrentWorkspaceDir(ctx); err != nil || got != dir {
			t.Fatalf("restricted=%t dir=%q err=%v", restricted, got, err)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixed directory was created: %v", err)
	}
	if store.clearCalls != 0 {
		t.Fatalf("fixed directory changed session: clears=%d", store.clearCalls)
	}
}

func TestCurrentWorkspaceDirRejectsRestrictedScopeWithoutDirectory(t *testing.T) {
	store := &fallbackWorkspaceStore{dir: t.TempDir(), readErr: errors.New("session must not be read")}
	ctx := WithPathScope(WithWorkspaceStore(t.Context(), store), PathScope{Restricted: true})
	if _, err := CurrentWorkspaceDir(ctx); err == nil || !strings.Contains(err.Error(), "restricted workspace directory") {
		t.Fatalf("missing restricted directory fell back to session: %v", err)
	}
}

func TestCurrentWorkspaceDirResetsDeletedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store := &fallbackWorkspaceStore{dir: dir}
	ctx := WithWorkspaceStore(context.Background(), store)
	if got, err := CurrentWorkspaceDir(ctx); err != nil || got != dir || store.clearCalls != 0 {
		t.Fatalf("existing workspace=%q err=%v clears=%d", got, err, store.clearCalls)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := CurrentWorkspaceDir(ctx); err != nil || got != cwd || store.dir != "" {
		t.Fatalf("fallback=%q err=%v saved=%q", got, err, store.dir)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := CurrentWorkspaceDir(ctx); err != nil || got != cwd || store.clearCalls != 1 {
		t.Fatalf("restored directory changed workspace=%q err=%v clears=%d", got, err, store.clearCalls)
	}
}

func TestCurrentWorkspaceDirRereadsConcurrentSelection(t *testing.T) {
	selected := t.TempDir()
	store := &fallbackWorkspaceStore{dir: filepath.Join(t.TempDir(), "missing")}
	// Interleave selection after validation but before conditional clearing.
	store.beforeClear = func() { store.dir = selected }
	got, err := CurrentWorkspaceDir(WithWorkspaceStore(context.Background(), store))
	if err != nil || got != selected || store.dir != selected || store.clearCalls != 1 {
		t.Fatalf("workspace=%q saved=%q err=%v clears=%d", got, store.dir, err, store.clearCalls)
	}
}

func TestCurrentWorkspaceDirPreservesStoreErrors(t *testing.T) {
	failure := errors.New("storage unavailable")
	for _, stage := range []string{"read", "clear"} {
		t.Run(stage, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "missing")
			store := &fallbackWorkspaceStore{dir: dir}
			if stage == "read" {
				store.readErr = failure
			} else {
				store.clearErr = failure
			}
			_, err := CurrentWorkspaceDir(WithWorkspaceStore(context.Background(), store))
			if !errors.Is(err, failure) || store.dir != dir {
				t.Fatalf("saved=%q err=%v", store.dir, err)
			}
		})
	}
}

func TestCurrentWorkspaceDirRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	store := &fallbackWorkspaceStore{dir: path}
	_, err := CurrentWorkspaceDir(WithWorkspaceStore(context.Background(), store))
	if err == nil || !strings.Contains(err.Error(), "not a directory") || store.clearCalls != 0 || store.dir != path {
		t.Fatalf("saved=%q clears=%d err=%v", store.dir, store.clearCalls, err)
	}
}

func TestCurrentWorkspaceDirPreservesPermissionError(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "workspace")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0700) })
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrPermission) {
		t.Skip("environment does not enforce directory permissions")
	}
	store := &fallbackWorkspaceStore{dir: dir}
	_, err := CurrentWorkspaceDir(WithWorkspaceStore(context.Background(), store))
	if !errors.Is(err, os.ErrPermission) || store.clearCalls != 0 || store.dir != dir {
		t.Fatalf("saved=%q clears=%d err=%v", store.dir, store.clearCalls, err)
	}
}

func TestCurrentWorkspaceDirKeepsSavedPathWhenDefaultUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not allow removing the current directory")
	}
	store := &fallbackWorkspaceStore{dir: filepath.Join(t.TempDir(), "missing")}
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	_, err := CurrentWorkspaceDir(WithWorkspaceStore(context.Background(), store))
	if err == nil || store.clearCalls != 0 || store.dir == "" {
		t.Fatalf("saved=%q clears=%d err=%v", store.dir, store.clearCalls, err)
	}
}
