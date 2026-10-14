package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"elbot/internal/events"
	"elbot/internal/hook"
	hookoutput "elbot/internal/hook/output"
	"elbot/internal/media"
	"elbot/internal/storage"
)

const mediaInlineBytes = 1024 * 1024
const mediaLeaseTTL = time.Hour

// Only this projection is sent over the pipe, never storage.Media itself.
type mediaResult struct {
	MediaID   string    `json:"media"`
	Name      string    `json:"name"`
	MIMEType  string    `json:"mime_type"`
	Size      int64     `json:"size"`
	Base64    string    `json:"base64,omitempty"`
	Path      string    `json:"path,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

type mediaLease struct {
	reference storage.MediaReference
	expires   time.Time
	dir       string
	path      string
}

type mediaBridge struct {
	mu     sync.Mutex
	api    hook.MediaAPI
	owner  string
	leases map[string]*mediaLease
	closed bool
}

func newMediaBridge(api hook.MediaAPI) *mediaBridge {
	return &mediaBridge{api: api, owner: randomID("hook-media"), leases: map[string]*mediaLease{}}
}

// MediaRequest is shared by once-exec and Worker Hook RPC dispatchers.
func (m *Manager) MediaRequest(ctx context.Context, baseDir, method string, raw json.RawMessage) (any, error) {
	if m.isClosed() {
		return nil, ErrClosed
	}
	return m.media.request(ctx, baseDir, method, raw)
}

func (b *mediaBridge) request(ctx context.Context, baseDir, method string, raw json.RawMessage) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrClosed
	}
	var metadata *storage.Media
	var err error
	switch method {
	case "media.import":
		var params struct {
			URL      string  `json:"url"`
			Path     string  `json:"path"`
			Base64   *string `json:"base64"`
			Name     string  `json:"name"`
			MIMEType string  `json:"mime_type"`
		}
		if err := hookoutput.DecodeJSON(raw, &params); err != nil {
			return nil, err
		}
		count := 0
		if params.URL != "" {
			count++
		}
		if params.Path != "" {
			count++
		}
		if params.Base64 != nil {
			count++
		}
		if count != 1 {
			return nil, fmt.Errorf("media.import requires exactly one of url, path or base64")
		}
		input := media.Input{Name: params.Name, MIMEType: params.MIMEType}
		switch {
		case params.URL != "":
			metadata, err = b.api.ImportURL(ctx, params.URL, input)
		case params.Path != "":
			// os.Root rejects traversal and symlinks escaping the plugin directory.
			if !filepath.IsLocal(params.Path) {
				return nil, fmt.Errorf("media.import path must be plugin-relative")
			}
			root, openErr := os.OpenRoot(baseDir)
			if openErr != nil {
				return nil, fmt.Errorf("open plugin directory: %w", openErr)
			}
			defer root.Close()
			file, openErr := root.Open(params.Path)
			if openErr != nil {
				return nil, fmt.Errorf("open plugin media: %w", openErr)
			}
			defer file.Close()
			info, statErr := file.Stat()
			if statErr != nil {
				return nil, fmt.Errorf("stat plugin media: %w", statErr)
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("media.import requires a regular file")
			}
			if input.Name == "" {
				input.Name = filepath.Base(params.Path)
			}
			metadata, err = b.api.ImportReader(ctx, file, info.Size(), input)
		default:
			if base64.StdEncoding.DecodedLen(len(*params.Base64)) > hookoutput.MaxBase64Bytes {
				return nil, fmt.Errorf("media.import base64 exceeds 10 MiB decoded limit")
			}
			var data []byte
			data, err = base64.StdEncoding.DecodeString(*params.Base64)
			if err == nil {
				metadata, err = b.api.ImportBytes(ctx, data, input)
			}
		}
	case "media.read", "media.export", "media.metadata":
		var params struct {
			MediaID string `json:"media"`
		}
		if err := hookoutput.DecodeJSON(raw, &params); err != nil {
			return nil, err
		}
		metadata, err = b.api.Metadata(ctx, params.MediaID)
	default:
		return nil, fmt.Errorf("unsupported hook media method %q", method)
	}
	if err != nil {
		return nil, fmt.Errorf("hook media operation failed: %w", err)
	}
	lease := b.leases[metadata.ID]
	if lease == nil {
		lease = &mediaLease{reference: storage.MediaReference{MediaID: metadata.ID, OwnerType: "hook", OwnerID: b.owner, Purpose: "temporary"}}
		if err := b.api.AddReference(ctx, &lease.reference); err != nil {
			return nil, err
		}
		b.leases[metadata.ID] = lease
	}
	lease.expires = time.Now().Add(mediaLeaseTTL)
	result := mediaResult{MediaID: metadata.ID, Name: metadata.Name, MIMEType: metadata.MIMEType, Size: metadata.Size, ExpiresAt: lease.expires}
	if method == "media.read" && metadata.Size <= mediaInlineBytes {
		data, _, err := b.api.Read(ctx, metadata.ID)
		if err != nil {
			return nil, err
		}
		result.Base64 = base64.StdEncoding.EncodeToString(data)
	} else if method == "media.export" || method == "media.read" {
		if lease.path == "" {
			dir, err := os.MkdirTemp("", "elbot-hook-media-*")
			if err != nil {
				return nil, err
			}
			path := filepath.Join(dir, "media")
			if _, err := b.api.Export(ctx, metadata.ID, path); err != nil {
				_ = os.RemoveAll(dir)
				return nil, err
			}
			lease.dir, lease.path = dir, path
		}
		result.Path = lease.path
	}
	return result, nil
}

func (b *mediaBridge) prune(ctx context.Context, now time.Time, closeAll bool) (events.LogResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if closeAll {
		b.closed = true
	}
	var failures []string
	logResult := events.LogResult{ResultStatus: events.ResultSkipped, Content: "no Hook media leases due"}
	for id, lease := range b.leases {
		if !closeAll && now.Before(lease.expires) {
			continue
		}
		if logResult.ResultStatus == events.ResultSkipped {
			logResult = events.LogResult{ResultStatus: events.ResultSucceeded, Content: "Hook media lease release"}
		}
		if err := b.api.RemoveReference(ctx, lease.reference); err != nil {
			failures = append(failures, err.Error())
			if !errors.Is(err, context.Canceled) {
				logResult.ResultStatus = events.ResultFailed
			} else if logResult.ResultStatus != events.ResultFailed {
				logResult.ResultStatus = events.ResultCanceled
			}
			logResult.Content += fmt.Sprintf("\nmedia_id=%s reference_release_error=%v", id, err)
			continue
		}
		logResult.Content += "\nmedia_id=" + id + " reference_released"
		if lease.dir != "" {
			if err := os.RemoveAll(lease.dir); err != nil {
				failures = append(failures, err.Error())
				logResult.ResultStatus = events.ResultFailed
				logResult.Content += fmt.Sprintf("\nmedia_id=%s directory_release_error=%v", id, err)
				continue
			}
		}
		delete(b.leases, id)
		logResult.Content += "\nmedia_id=" + id + " released"
	}
	if len(failures) > 0 {
		return logResult, fmt.Errorf("release hook media: %s", strings.Join(failures, "; "))
	}
	return logResult, nil
}
