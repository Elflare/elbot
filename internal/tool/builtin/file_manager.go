package builtin

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/config"
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

func (m *FileManager) Prepare(sourcePath, name, mimeType string) (preparedFile, error) {
	if m == nil {
		return preparedFile{}, fmt.Errorf("file manager is not configured")
	}
	source := filepath.Clean(strings.TrimSpace(sourcePath))
	if source == "" {
		return preparedFile{}, fmt.Errorf("path is required")
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
