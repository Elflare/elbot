package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/config"
	"elbot/internal/storage"
)

func NewManager(store storage.Store, root string, backend Backend) *Manager {
	defaults := config.Default()
	return &Manager{Store: store, Root: filepath.Clean(root), Backend: backend, Now: storage.Now, FileDelivery: defaults.FileDelivery, MaxImportBytes: defaults.PlatformFiles.MaxReceiveFileBytes, DownloadTimeout: time.Duration(defaults.PlatformFiles.DownloadTimeoutSecs) * time.Second}
}

func (m *Manager) ImportBytes(ctx context.Context, data []byte, input Input) (*storage.Media, error) {
	return m.ImportReader(ctx, bytes.NewReader(data), int64(len(data)), input)
}

func (m *Manager) ImportURL(ctx context.Context, rawURL string, input Input) (*storage.Media, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid media URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create media request: %w", err)
	}
	response, err := (&http.Client{Timeout: m.DownloadTimeout}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("download media: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("download media: unexpected HTTP status %s", response.Status)
	}
	if input.Source.URL == "" {
		input.Source.URL = u.String()
	}
	if input.MIMEType == "" {
		input.MIMEType = response.Header.Get("Content-Type")
	}
	if input.Name == "" {
		input.Name = filepath.Base(u.Path)
	}
	return m.ImportReader(ctx, response.Body, response.ContentLength, input)
}

func (m *Manager) ImportFile(ctx context.Context, path string, input Input) (*storage.Media, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open media source: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat media source: %w", err)
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("media source is not a regular file")
	}
	if input.Name == "" {
		input.Name = filepath.Base(path)
	}
	return m.ImportReader(ctx, file, info.Size(), input)
}

func (m *Manager) ImportReader(ctx context.Context, input io.Reader, size int64, spec Input) (*storage.Media, error) {
	if size > m.MaxImportBytes {
		return nil, fmt.Errorf("media exceeds import limit of %d bytes", m.MaxImportBytes)
	}
	data, err := io.ReadAll(io.LimitReader(input, m.MaxImportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read media: %w", err)
	}
	size = int64(len(data))
	if size > m.MaxImportBytes {
		return nil, fmt.Errorf("media exceeds import limit of %d bytes", m.MaxImportBytes)
	}
	sum := sha256.Sum256(data)
	id := fmt.Sprintf("%s%x", IDPrefix, sum)
	if existing, err := m.Store.Media().Get(ctx, id); err == nil {
		return existing, nil
	} else if err != storage.ErrNotFound {
		return nil, err
	}
	if spec.MIMEType == "" {
		spec.MIMEType = mime.TypeByExtension(filepath.Ext(spec.Name))
	}
	if spec.MIMEType == "" {
		spec.MIMEType = http.DetectContentType(data)
	}
	if spec.Name == "" {
		spec.Name = "file"
	}
	// Signed and inline transport URLs are never persisted as source metadata.
	if u, err := url.Parse(spec.Source.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" {
		spec.Source.URL = ""
	}
	media := &storage.Media{ID: id, Name: filepath.Base(spec.Name), MIMEType: spec.MIMEType, Size: size, Backend: backendName(m.Backend), SourcePlatform: spec.Source.Platform, SourceURL: spec.Source.URL, SourceFileID: spec.Source.FileID}
	location, err := m.Backend.Put(ctx, id, bytes.NewReader(data), size, media.MIMEType)
	if err != nil {
		return nil, err
	}
	if media.Backend == "s3" {
		media.ObjectKey = location
	} else {
		media.LocalPath = location
	}
	if err := m.Store.Media().Upsert(ctx, media); err != nil {
		_ = m.Backend.Remove(ctx, media)
		return nil, err
	}
	return media, nil
}

func (m *Manager) Open(ctx context.Context, id string) (io.ReadCloser, *storage.Media, error) {
	if !ValidID(id) {
		return nil, nil, fmt.Errorf("invalid media ID %q", id)
	}
	media, err := m.Store.Media().Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	backend := m.Backend
	if media.Backend == "local" {
		backend = &LocalBackend{Root: m.Root}
	} else if m.Remote != nil {
		backend = m.Remote
	}
	reader, err := backend.Open(ctx, media)
	if err != nil {
		return nil, nil, err
	}
	return reader, media, nil
}

func (m *Manager) Read(ctx context.Context, id string) ([]byte, *storage.Media, error) {
	reader, media, err := m.Open(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("read media %q: %w", id, err)
	}
	return data, media, nil
}

func (m *Manager) Export(ctx context.Context, id, destination string) (*storage.Media, error) {
	reader, media, err := m.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	file, err := os.Create(destination)
	if err != nil {
		return nil, fmt.Errorf("create media export: %w", err)
	}
	if _, err := io.Copy(file, reader); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("export media: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close media export: %w", err)
	}
	return media, nil
}

func (m *Manager) AddReference(ctx context.Context, reference *storage.MediaReference) error {
	if reference == nil || !ValidID(reference.MediaID) {
		return fmt.Errorf("invalid media reference")
	}
	if _, err := m.Store.Media().Get(ctx, reference.MediaID); err != nil {
		return err
	}
	return m.Store.MediaReferences().Add(ctx, reference)
}

func (m *Manager) RemoveReference(ctx context.Context, reference storage.MediaReference) error {
	if !ValidID(reference.MediaID) {
		return fmt.Errorf("invalid media reference")
	}
	return m.Store.MediaReferences().Remove(ctx, reference)
}

func (m *Manager) PresignGet(ctx context.Context, id string, expiry time.Duration) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid media ID %q", id)
	}
	if expiry <= 0 {
		expiry = time.Hour
	}
	metadata, err := m.Store.Media().Get(ctx, id)
	if err != nil {
		return "", err
	}
	backend := m.Remote
	if backend == nil {
		backend = m.Backend
	}
	if metadata.ObjectKey == "" && backend != m.Backend {
		reader, _, err := m.Open(ctx, id)
		if err != nil {
			return "", err
		}
		defer reader.Close()
		metadata.ObjectKey, err = backend.Put(ctx, id, reader, metadata.Size, metadata.MIMEType)
		if err != nil {
			return "", err
		}
		if err := m.Store.Media().Upsert(ctx, metadata); err != nil {
			return "", err
		}
	}
	return backend.PresignGet(ctx, metadata, expiry)
}

func backendName(backend Backend) string {
	switch backend.(type) {
	case *S3Backend:
		return "s3"
	default:
		return "local"
	}
}

func ValidID(id string) bool {
	if !strings.HasPrefix(id, IDPrefix) || len(id) != len(IDPrefix)+64 {
		return false
	}
	for _, char := range id[len(IDPrefix):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
