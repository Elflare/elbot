package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type workspaceContextKey struct{}

const absolutePathWorkspaceWarning = "使用了绝对路径；本次按该路径执行，但不会改变 workspace。"

type WorkspaceStore interface {
	GetWorkspaceDir(ctx context.Context) (string, error)
	SetWorkspaceDir(ctx context.Context, dir string) error
	ClearWorkspaceDir(ctx context.Context) error
}

type PathResolveOptions struct {
	AllowCreate    bool
	AllowDirectory bool
}

type ResolvedPath struct {
	Path     string
	BaseDir  string
	WasAbs   bool
	Warnings []string
}

func WithWorkspaceStore(ctx context.Context, store WorkspaceStore) context.Context {
	if store == nil {
		return ctx
	}
	return context.WithValue(ctx, workspaceContextKey{}, store)
}

func WorkspaceStoreFromContext(ctx context.Context) (WorkspaceStore, bool) {
	store, ok := ctx.Value(workspaceContextKey{}).(WorkspaceStore)
	return store, ok
}

func CurrentWorkspaceDir(ctx context.Context) (string, error) {
	if store, ok := WorkspaceStoreFromContext(ctx); ok {
		dir, err := store.GetWorkspaceDir(ctx)
		if err != nil {
			return "", fmt.Errorf("load workspace: %w", err)
		}
		if strings.TrimSpace(dir) != "" {
			return ValidateWorkspaceDir(dir)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	return filepath.Clean(cwd), nil
}

func ResolveWorkspacePath(ctx context.Context, rawPath string, opts PathResolveOptions) (ResolvedPath, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return ResolvedPath{}, fmt.Errorf("path is required")
	}
	if sandbox, ok := SandboxContextFromContext(ctx); ok && sandbox.Background {
		path, err := ResolveSandboxRelativePath(sandbox, rawPath)
		if err != nil {
			return ResolvedPath{}, err
		}
		if err := validateResolvedPath(path, opts); err != nil {
			return ResolvedPath{}, err
		}
		return ResolvedPath{Path: filepath.Clean(path), BaseDir: filepath.Clean(firstNonEmptyString(sandbox.Dir, sandbox.Root))}, nil
	}
	expandedPath, err := expandWorkspacePath(rawPath)
	if err != nil {
		return ResolvedPath{}, err
	}
	path := normalizeWorkspaceLocalPath(expandedPath)
	wasAbs := filepath.IsAbs(path)
	baseDir, err := CurrentWorkspaceDir(ctx)
	if err != nil {
		return ResolvedPath{}, err
	}
	if !wasAbs {
		path = filepath.Join(baseDir, path)
	}
	path = filepath.Clean(path)
	if err := validateResolvedPath(path, opts); err != nil {
		return ResolvedPath{}, err
	}
	resolved := ResolvedPath{Path: path, BaseDir: baseDir, WasAbs: wasAbs}
	if wasAbs {
		resolved.Warnings = []string{absolutePathWorkspaceWarning}
	}
	return resolved, nil
}

func ValidateWorkspaceDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("workspace path is required")
	}
	expandedPath, err := expandWorkspacePath(path)
	if err != nil {
		return "", err
	}
	path = filepath.Clean(normalizeWorkspaceLocalPath(expandedPath))
	if path == "" || path == "." {
		return "", fmt.Errorf("workspace path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory: %s", path)
	}
	return path, nil
}

func expandWorkspacePath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("home directory is not configured")
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func validateResolvedPath(path string, opts PathResolveOptions) error {
	info, err := os.Stat(path)
	if err != nil {
		if opts.AllowCreate && os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() && !opts.AllowDirectory {
		return fmt.Errorf("path is a directory")
	}
	return nil
}

func normalizeWorkspaceLocalPath(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	path = strings.ReplaceAll(path, "\\", "/")
	if len(path) >= 3 && path[0] == '/' && path[2] == '/' && isASCIIAlpha(path[1]) {
		return strings.ToUpper(string(path[1])) + ":" + filepath.FromSlash(path[2:])
	}
	return filepath.FromSlash(path)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
