// 统一解析调用范围或 Session 工作目录中的文件路径。
package workspace

import (
	"context"
	"elbot/internal/operationresult"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func ResolveWorkspacePath(ctx context.Context, rawPath string, opts PathResolveOptions) (operationresult.Result[ResolvedPath], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[ResolvedPath]{}, err
	}
	if scope, ok := PathScopeFromContext(ctx); ok && scope.Restricted {
		relative, err := NormalizeRestrictedPath(rawPath)
		if err != nil {
			return operationresult.Result[ResolvedPath]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
		}
		baseDir, err := CurrentWorkspaceDir(ctx)
		if err != nil {
			return operationresult.Result[ResolvedPath]{}, err
		}
		path := filepath.Join(baseDir, relative)
		checked, err := validateResolvedPath(path, opts)
		if err != nil {
			return operationresult.Result[ResolvedPath]{}, err
		}
		if checked.Status != operationresult.Success {
			return operationresult.Result[ResolvedPath]{Status: checked.Status, Message: checked.Message}, nil
		}
		return operationresult.Result[ResolvedPath]{Status: operationresult.Success, Data: ResolvedPath{Path: path, BaseDir: baseDir}}, nil
	}
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return operationresult.Result[ResolvedPath]{Status: operationresult.InvalidArguments, Message: "path is required"}, nil
	}
	expandedPath, err := expandWorkspacePath(rawPath)
	if err != nil {
		return operationresult.Result[ResolvedPath]{}, err
	}
	path := normalizeWorkspaceLocalPath(expandedPath)
	wasAbs := filepath.IsAbs(path)
	baseDir, err := CurrentWorkspaceDir(ctx)
	if err != nil {
		return operationresult.Result[ResolvedPath]{}, err
	}
	if !wasAbs {
		path = filepath.Join(baseDir, path)
	}
	path = filepath.Clean(path)
	checked, err := validateResolvedPath(path, opts)
	if err != nil {
		return operationresult.Result[ResolvedPath]{}, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[ResolvedPath]{Status: checked.Status, Message: checked.Message}, nil
	}
	return operationresult.Result[ResolvedPath]{Status: operationresult.Success, Data: ResolvedPath{Path: path, BaseDir: baseDir, WasAbs: wasAbs}}, nil
}

func ValidateWorkspaceDir(path string) (operationresult.Result[string], error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return operationresult.Result[string]{Status: operationresult.InvalidArguments, Message: "workspace path is required"}, nil
	}
	expandedPath, err := expandWorkspacePath(path)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	path = filepath.Clean(normalizeWorkspaceLocalPath(expandedPath))
	if path == "" || path == "." {
		return operationresult.Result[string]{Status: operationresult.InvalidArguments, Message: "workspace path is required"}, nil
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return operationresult.Result[string]{}, fmt.Errorf("resolve workspace path: %w", err)
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return operationresult.Result[string]{Status: operationresult.NotFound, Message: fmt.Sprintf("workspace path not found: %s", path)}, nil
	}
	if err != nil {
		return operationresult.Result[string]{}, fmt.Errorf("resolve workspace path: %w", err)
	}
	if !info.IsDir() {
		return operationresult.Result[string]{Status: operationresult.InvalidTarget, Message: fmt.Sprintf("workspace path is not a directory: %s", path)}, nil
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: path}, nil
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

func validateResolvedPath(path string, opts PathResolveOptions) (operationresult.Result[struct{}], error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		if opts.AllowCreate {
			return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
		}
		return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: fmt.Sprintf("path not found: %s", path)}, nil
	}
	if err != nil {
		return operationresult.Result[struct{}]{}, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() && !opts.AllowDirectory {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidTarget, Message: "path is a directory"}, nil
	}
	if opts.RequireRegularFile && !info.Mode().IsRegular() {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidTarget, Message: "path is not a regular file"}, nil
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
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
