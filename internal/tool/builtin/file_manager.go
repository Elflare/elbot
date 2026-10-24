// 管理工具使用的文件路径、媒体及文件发送能力。
package builtin

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/media"
)

type FileManager struct {
	SandboxRoot string
	Media       *media.Manager
}

type preparedFile struct {
	Path     string
	Name     string
	MIMEType string
}

func NewFileManager(sandboxRoot string) *FileManager {
	return &FileManager{SandboxRoot: filepath.Clean(sandboxRoot)}
}

func (m *FileManager) Prepare(sourcePath, name, mimeType string) (preparedFile, error) {
	source := filepath.Clean(strings.TrimSpace(sourcePath))
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
	return preparedFile{Path: source, Name: fileName, MIMEType: mimeType}, nil
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
