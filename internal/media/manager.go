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
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"elbot/internal/config"
	"elbot/internal/storage"
)

func NewManager(store storage.Store, root string, backend Backend) *Manager {
	defaults := config.Default()
	root = filepath.Clean(root)
	local := Backend(&LocalBackend{Root: root})
	if backend != nil && backendName(backend) == "local" {
		local = backend
	}
	return &Manager{objects: &sync.Mutex{}, local: local, Store: store, Root: root, Backend: backend, Now: storage.Now, FileDelivery: defaults.FileDelivery, MaxImportBytes: defaults.PlatformFiles.MaxReceiveFileBytes, DownloadTimeout: time.Duration(defaults.PlatformFiles.DownloadTimeoutSecs) * time.Second}
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
	m.objects.Lock()
	defer m.objects.Unlock()
	if existing, err := m.Store.Media().Get(ctx, id); err == nil {
		if existing.Deleting {
			return nil, fmt.Errorf("media is being deleted")
		}
		if err := m.Store.Media().Touch(ctx, id, m.Now()); err != nil {
			return nil, err
		}
		return sanitizeMediaMetadata(existing), nil
	} else if err != storage.ErrNotFound {
		return nil, err
	}
	spec = sanitizeInput(spec)
	if spec.MIMEType == "" {
		spec.MIMEType = mime.TypeByExtension(filepath.Ext(spec.Name))
	}
	if spec.MIMEType == "" {
		spec.MIMEType = http.DetectContentType(data)
	}
	media := &storage.Media{ID: id, Name: spec.Name, MIMEType: spec.MIMEType, Size: size, Backend: backendName(m.Backend), SourcePlatform: spec.Source.Platform, SourceURL: spec.Source.URL, SourceFileID: spec.Source.FileID}
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
	if media.Deleting {
		return nil, nil, fmt.Errorf("media is being deleted")
	}
	backend, err := m.backendForStoredMedia(media)
	if err != nil {
		return nil, nil, err
	}
	if err := m.Store.Media().Touch(ctx, id, m.Now()); err != nil {
		return nil, nil, err
	}
	release, err := m.Hold(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	reader, err := backend.Open(ctx, media)
	if err != nil {
		_ = release()
		return nil, nil, err
	}
	return &referencedReader{ReadCloser: reader, release: release}, sanitizeMediaMetadata(media), nil
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
	if metadata.Deleting {
		return "", fmt.Errorf("media is being deleted")
	}
	backend, err := m.remoteBackend()
	if err != nil {
		return "", err
	}
	// Stored media may still be local after the configured backend switches to S3.
	if metadata.ObjectKey == "" && metadata.Backend != "s3" {
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

func sanitizeInput(input Input) Input {
	input.Name = sanitizeMediaName(input.Name)
	input.Source.URL = sanitizeSourceURL(input.Source.URL)
	input.Source.FileID = sanitizeSourceFileID(input.Source.FileID)
	return input
}

func sanitizeMediaMetadata(item *storage.Media) *storage.Media {
	if item == nil {
		return nil
	}
	safe := *item
	safe.Name = sanitizeMediaName(safe.Name)
	safe.SourceURL = sanitizeSourceURL(safe.SourceURL)
	safe.SourceFileID = sanitizeSourceFileID(safe.SourceFileID)
	return &safe
}

// SanitizeName returns a basename suitable for persisted and model-visible media labels.
func SanitizeName(value string) string {
	return sanitizeMediaName(value)
}

func sanitizeMediaName(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if value == "" || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "base64:") {
		return "file"
	}
	if parsed, err := url.Parse(value); err == nil {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "file":
			value = parsed.Path
		}
	}
	value = path.Base(strings.ReplaceAll(value, "\\", "/"))
	if decoded, err := url.PathUnescape(value); err == nil {
		value = path.Base(strings.ReplaceAll(decoded, "\\", "/"))
	}
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || value == "/" {
		return "file"
	}
	return value
}

func sanitizeSourceURL(value string) string {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return ""
	}
	for _, component := range strings.Split(parsed.Path, "/") {
		component = strings.ToLower(component)
		if strings.HasPrefix(component, "bot") && strings.Contains(component, ":") {
			return ""
		}
	}
	return parsed.String()
}

func sanitizeSourceFileID(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if value == "" || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "base64:") ||
		strings.Contains(value, "://") || strings.ContainsAny(value, "/\\") {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "file":
			return ""
		}
	}
	if len(value) >= 2 && value[1] == ':' &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) {
		return ""
	}
	return value
}

func (m *Manager) backendForStoredMedia(item *storage.Media) (Backend, error) {
	switch item.Backend {
	case "local":
		if m.local != nil {
			return m.local, nil
		}
		return &LocalBackend{Root: m.Root}, nil
	case "s3":
		return m.remoteBackend()
	default:
		return nil, fmt.Errorf("unsupported stored media backend %q", item.Backend)
	}
}

func (m *Manager) remoteBackend() (Backend, error) {
	if m.Remote != nil {
		return m.Remote, nil
	}
	if backendName(m.Backend) == "s3" {
		return m.Backend, nil
	}
	return nil, fmt.Errorf("remote media backend is unavailable")
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
