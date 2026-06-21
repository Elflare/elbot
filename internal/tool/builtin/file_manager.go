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
	Config      config.FileDeliveryConfig
}

type preparedFile struct {
	Path     string
	Name     string
	MIMEType string
	Size     int64
}

func NewFileManager(sandboxRoot string, cfg config.FileDeliveryConfig) *FileManager {
	if strings.TrimSpace(sandboxRoot) == "" {
		sandboxRoot = config.Default().Sandbox.Root
	}
	defaults := config.Default().FileDelivery
	if cfg.MaxDirectBase64Bytes <= 0 {
		cfg.MaxDirectBase64Bytes = defaults.MaxDirectBase64Bytes
	}
	if strings.TrimSpace(cfg.Backend) == "" {
		cfg.Backend = defaults.Backend
	}
	if strings.TrimSpace(cfg.S3Region) == "" {
		cfg.S3Region = defaults.S3Region
	}
	return &FileManager{SandboxRoot: filepath.Clean(sandboxRoot), Config: cfg}
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
	if err := m.checkSize(source, info); err != nil {
		return preparedFile{}, err
	}
	fileName := safeFileName(firstNonEmptyString(name, filepath.Base(source)))
	if mimeType = strings.TrimSpace(mimeType); mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(fileName))
	}
	return preparedFile{Path: source, Name: fileName, MIMEType: mimeType, Size: info.Size()}, nil
}

func (m *FileManager) checkSize(source string, info os.FileInfo) error {
	if m.Config.MaxDirectBase64Bytes <= 0 || info.Size() <= m.Config.MaxDirectBase64Bytes {
		return nil
	}
	switch strings.TrimSpace(m.Config.Backend) {
	case "", "base64":
		return fmt.Errorf("file %q is %d bytes, exceeds file_delivery.max_direct_base64_bytes=%d; configure future s3/r2 backend for large files", source, info.Size(), m.Config.MaxDirectBase64Bytes)
	default:
		return fmt.Errorf("file delivery backend %q is not implemented yet", m.Config.Backend)
	}
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
