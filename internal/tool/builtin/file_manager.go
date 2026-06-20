package builtin

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"elbot/internal/config"
	"elbot/internal/tool"
)

type FileManager struct {
	SandboxRoot string
}

type preparedFile struct {
	Path     string
	Name     string
	MIMEType string
	Size     int64
}

func NewFileManager(sandboxRoot string) *FileManager {
	if strings.TrimSpace(sandboxRoot) == "" {
		sandboxRoot = config.Default().Sandbox.Root
	}
	return &FileManager{SandboxRoot: filepath.Clean(sandboxRoot)}
}

func (m *FileManager) Prepare(ctxSandbox tool.SandboxContext, sourcePath, name, mimeType string) (preparedFile, error) {
	if m == nil {
		return preparedFile{}, fmt.Errorf("file manager is not configured")
	}
	source, err := m.resolveSource(ctxSandbox, sourcePath)
	if err != nil {
		return preparedFile{}, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return preparedFile{}, fmt.Errorf("stat file %q: %w", source, err)
	}
	if info.IsDir() {
		return preparedFile{}, fmt.Errorf("file %q is a directory", source)
	}
	if !info.Mode().IsRegular() {
		return preparedFile{}, fmt.Errorf("file %q is not a regular file", source)
	}
	fileName := safeFileName(firstNonEmptyString(name, filepath.Base(source)))
	if mimeType = strings.TrimSpace(mimeType); mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(fileName))
	}
	return preparedFile{Path: source, Name: fileName, MIMEType: mimeType, Size: info.Size()}, nil
}

func (m *FileManager) resolveSource(ctxSandbox tool.SandboxContext, sourcePath string) (string, error) {
	sourcePath = normalizeLocalPath(strings.TrimSpace(sourcePath))
	if sourcePath == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(sourcePath) {
		return filepath.Clean(sourcePath), nil
	}
	if strings.TrimSpace(ctxSandbox.Dir) != "" {
		return filepath.Clean(filepath.Join(ctxSandbox.Dir, sourcePath)), nil
	}
	return filepath.Abs(sourcePath)
}

func normalizeLocalPath(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	path = strings.ReplaceAll(path, "\\", "/")
	if len(path) >= 3 && path[0] == '/' && path[2] == '/' && isASCIIAlpha(path[1]) {
		return strings.ToUpper(string(path[1])) + ":" + filepath.FromSlash(path[2:])
	}
	return filepath.FromSlash(path)
}

func isASCIIAlpha(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

func safeFileName(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "file"
	}
	return name
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
