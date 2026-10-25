// 提供沙盒目录与受限路径解析能力。
package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
)

// NormalizeRestrictedPath 返回规范化的本机相对路径，不访问文件系统。
// 两种斜杠都作为分隔符；禁止绝对路径、盘符、冒号、~ 和任意 .. 路径段。
func NormalizeRestrictedPath(rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.HasPrefix(rawPath, "~") {
		return "", fmt.Errorf("restricted path must be relative and must not use ~")
	}
	slashPath := strings.ReplaceAll(rawPath, "\\", "/")
	if strings.HasPrefix(slashPath, "/") || strings.Contains(slashPath, ":") {
		return "", fmt.Errorf("restricted path must be relative and must not contain a colon")
	}
	for _, part := range strings.Split(slashPath, "/") {
		if part == ".." {
			return "", fmt.Errorf("restricted path must not contain parent traversal")
		}
	}
	path := filepath.FromSlash(slashPath)
	if !filepath.IsLocal(path) {
		return "", fmt.Errorf("restricted path must be relative")
	}
	return filepath.Clean(path), nil
}

// SandboxDir 从配置根目录和业务提供的相对子目录计算绝对路径，不创建目录。
func SandboxDir(root, subdir string) (string, error) {
	return ResolveSandboxRelativePath(PathScope{Dir: root, Restricted: true}, subdir)
}

// ResolveSandboxRelativePath 总是按受限规则解析 Dir 内的路径，不依赖 Restricted 开关。
// 只检查路径组成，不创建或打开文件。
func ResolveSandboxRelativePath(scope PathScope, rawPath string) (string, error) {
	path, err := NormalizeRestrictedPath(rawPath)
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(scope.Dir)
	if root == "" {
		return "", fmt.Errorf("restricted workspace directory is not configured")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve restricted workspace: %w", err)
	}
	return filepath.Join(rootAbs, path), nil
}
