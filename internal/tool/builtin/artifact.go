package builtin

import (
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"elbot/internal/config"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type ArtifactManager struct {
	SandboxRoot string
	ArtifactDir string
	Config      config.ArtifactConfig
}

type preparedArtifact struct {
	Path     string
	Name     string
	MIMEType string
	Size     int64
}

func NewArtifactManager(sandboxRoot string, cfg config.ArtifactConfig) *ArtifactManager {
	if strings.TrimSpace(sandboxRoot) == "" {
		sandboxRoot = config.Default().Sandbox.Root
	}
	defaults := config.Default().Artifact
	if cfg.RetentionDays == 0 {
		cfg.RetentionDays = defaults.RetentionDays
	}
	if cfg.MaxDirectBase64Bytes <= 0 {
		cfg.MaxDirectBase64Bytes = defaults.MaxDirectBase64Bytes
	}
	if strings.TrimSpace(cfg.Backend) == "" {
		cfg.Backend = defaults.Backend
	}
	if strings.TrimSpace(cfg.S3Region) == "" {
		cfg.S3Region = defaults.S3Region
	}
	return &ArtifactManager{SandboxRoot: filepath.Clean(sandboxRoot), ArtifactDir: filepath.Join(filepath.Clean(sandboxRoot), "artifact"), Config: cfg}
}

func (m *ArtifactManager) Prepare(ctxSandbox tool.SandboxContext, sourcePath, name, mimeType string) (preparedArtifact, error) {
	if m == nil {
		return preparedArtifact{}, fmt.Errorf("artifact manager is not configured")
	}
	if strings.TrimSpace(m.Config.Backend) != "" && m.Config.Backend != "base64" {
		return preparedArtifact{}, fmt.Errorf("artifact backend %q is not implemented yet", m.Config.Backend)
	}
	source, err := m.resolveSource(ctxSandbox, sourcePath)
	if err != nil {
		return preparedArtifact{}, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return preparedArtifact{}, fmt.Errorf("stat file %q: %w", source, err)
	}
	if info.IsDir() {
		return preparedArtifact{}, fmt.Errorf("file %q is a directory", source)
	}
	if !info.Mode().IsRegular() {
		return preparedArtifact{}, fmt.Errorf("file %q is not a regular file", source)
	}
	if m.Config.MaxDirectBase64Bytes > 0 && info.Size() > m.Config.MaxDirectBase64Bytes {
		return preparedArtifact{}, fmt.Errorf("file %q is %d bytes, exceeds artifact.max_direct_base64_bytes=%d; configure future s3/r2 backend for large files", source, info.Size(), m.Config.MaxDirectBase64Bytes)
	}

	fileName := safeArtifactName(firstNonEmptyString(name, filepath.Base(source)))
	artifactPath := source
	if !isPathWithin(source, m.SandboxRoot) {
		artifactPath = filepath.Join(m.ArtifactDir, storage.NewID(), fileName)
		if err := copyFile(source, artifactPath); err != nil {
			return preparedArtifact{}, err
		}
	}
	if mimeType = strings.TrimSpace(mimeType); mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(fileName))
	}
	return preparedArtifact{Path: artifactPath, Name: fileName, MIMEType: mimeType, Size: info.Size()}, nil
}

func (m *ArtifactManager) resolveSource(ctxSandbox tool.SandboxContext, sourcePath string) (string, error) {
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open file %q: %w", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create artifact dir %q: %w", filepath.Dir(dst), err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create artifact %q: %w", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy artifact %q: %w", dst, err)
	}
	return nil
}

func safeArtifactName(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "artifact"
	}
	return name
}

func isPathWithin(path, root string) bool {
	pathAbs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
