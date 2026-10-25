// 统一解析调用范围或 Session 工作目录中的文件路径。
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func ResolveWorkspacePath(ctx context.Context, rawPath string, opts PathResolveOptions) (ResolvedPath, error) {
	if err := ctx.Err(); err != nil {
		return ResolvedPath{}, err
	}
	if scope, ok := PathScopeFromContext(ctx); ok && scope.Restricted {
		path, err := ResolveSandboxRelativePath(scope, rawPath)
		if err != nil {
			return ResolvedPath{}, err
		}
		if err := validateResolvedPath(path, opts); err != nil {
			return ResolvedPath{}, err
		}
		baseDir, err := filepath.Abs(strings.TrimSpace(scope.Dir))
		if err != nil {
			return ResolvedPath{}, err
		}
		return ResolvedPath{Path: path, BaseDir: baseDir}, nil
	}
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return ResolvedPath{}, fmt.Errorf("path is required")
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
	return ResolvedPath{Path: path, BaseDir: baseDir, WasAbs: wasAbs}, nil
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
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
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
	isHomePath := func(value, prefix string) bool {
		return value == prefix || strings.HasPrefix(value, prefix+"/") || strings.HasPrefix(value, prefix+`\`)
	}
	prefix := "~"
	if !isHomePath(path, prefix) {
		prefix = "$HOME"
		if !isHomePath(path, prefix) {
			return path, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("home directory is not configured")
	}
	if path == prefix {
		return home, nil
	}
	return filepath.Join(home, path[len(prefix)+1:]), nil
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
	if opts.RequireRegularFile && !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
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

func isASCIIAlpha(ch byte) bool { return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' }
