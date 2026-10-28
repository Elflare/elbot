// 处理插件媒体 RPC、租约、引用、临时文件及清理。

package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/events"
	hookoutput "elbot/internal/hook/share/output"
	"elbot/internal/llm"
	"elbot/internal/media"
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
	reference media.Reference // 租约对应的持久引用身份。
	expires   time.Time       // 最后一次使用后的到期时间，由集合锁保护。
	dir       string          // 租约拥有的临时目录，由占用者独占访问。
	path      string          // 已完成导出的路径，由占用者独占访问。
	retained  bool            // 引用已取得且尚未成功释放，避免重复清理成功项。
	busy      chan struct{}   // 当前独占操作结束时关闭；等待者可取消。
	users     int             // 正在使用本租约的调用数，当前独占操作为 0 或 1。
	cleaning  bool            // 已被维护认领，新使用等待其结束后重新查找。
}

// mediaAPI is runtime's borrowed media capability, limited to plugin RPC and leases.
type mediaAPI interface {
	ImportFile(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
	ImportURL(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
	ImportReader(context.Context, io.Reader, int64, media.Input, media.ImportLimits) (media.MediaInfo, error)
	ImportBytes(context.Context, []byte, media.Input, media.ImportLimits) (media.MediaInfo, error)
	Info(context.Context, string) (media.MediaInfo, error)
	Read(context.Context, string) ([]byte, media.MediaInfo, error)
	Export(context.Context, string, string) (media.MediaInfo, error)
	AddReference(context.Context, media.Reference) error
	RemoveReference(context.Context, media.Reference) error
}

type mediaBridge struct {
	mu     sync.Mutex
	api    mediaAPI
	owner  string
	leases map[string]*mediaLease
	closed bool
}

func newMediaBridge(api mediaAPI) *mediaBridge {
	return &mediaBridge{api: api, owner: randomID("hook-media"), leases: map[string]*mediaLease{}}
}

// MediaRequest is shared by once-exec and Worker Hook RPC dispatchers.
func (m *Manager) MediaRequest(ctx context.Context, baseDir, method string, raw json.RawMessage) (any, error) {
	ctx, finish, err := m.beginMediaCall(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	return m.media.request(ctx, baseDir, method, raw)
}

// BuildMessageSegments 归档已分类来源，租约由运行时持有。
func (m *Manager) BuildMessageSegments(ctx context.Context, specs []hookoutput.MessageSegment, baseDir string) ([]llm.MessageSegment, error) {
	ctx, finish, err := m.beginMediaCall(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	outputs, err := hookoutput.BuildMessageSegments(specs, baseDir)
	if err != nil {
		return nil, err
	}
	b := m.media
	segments := make([]llm.MessageSegment, 0, len(outputs))
	for i, out := range outputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if out.Kind == delivery.KindText {
			segments = append(segments, llm.MessageSegment{Type: llm.SegmentText, Text: out.Text})
			continue
		}
		input := media.Input{Name: out.Name, MIMEType: out.Source.MIMEType}
		limits := media.ImportLimits{MaxImportBytes: hookoutput.MaxBase64Bytes}
		var metadata media.MediaInfo
		switch {
		case out.Source.MediaID != "":
			metadata, err = b.api.Info(ctx, out.Source.MediaID)
		case out.Source.URL != "":
			metadata, err = b.api.ImportURL(ctx, out.Source.URL, input, limits)
		case out.Source.Path != "":
			metadata, err = b.api.ImportFile(ctx, out.Source.Path, input, limits)
		default:
			metadata, err = b.api.ImportBytes(ctx, out.Source.Data, input, limits)
		}
		if err != nil {
			return nil, fmt.Errorf("message.segments[%d]: %w", i, err)
		}
		if !strings.HasPrefix(metadata.MIMEType, "image/") {
			return nil, fmt.Errorf("message.segments[%d]: media must be an image", i)
		}
		lease, err := b.retain(ctx, metadata.ID)
		if err != nil {
			return nil, err
		}
		b.release(lease)
		segments = append(segments, llm.MessageSegment{Type: llm.SegmentImage, Text: out.Text, MediaID: metadata.ID, Name: metadata.Name, MIMEType: metadata.MIMEType})
	}
	return segments, ctx.Err()
}

// retain 在集合锁内认领租约，引用 I/O 在锁外完成；同租约等待可取消。
func (b *mediaBridge) retain(ctx context.Context, id string) (*mediaLease, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return nil, ErrClosed
		}
		lease := b.leases[id]
		if lease != nil && lease.busy != nil {
			done := lease.busy
			b.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}
		if lease == nil {
			lease = &mediaLease{reference: media.Reference{MediaID: id, OwnerType: "hook", OwnerID: b.owner, Purpose: "temporary"}}
			b.leases[id] = lease
		}
		lease.busy, lease.users = make(chan struct{}), 1
		lease.expires = time.Now().Add(mediaLeaseTTL)
		b.mu.Unlock()
		if !lease.retained {
			if err := b.api.AddReference(ctx, lease.reference); err != nil {
				b.mu.Lock()
				if lease.dir == "" {
					delete(b.leases, id)
				}
				lease.users = 0
				close(lease.busy)
				lease.busy = nil
				b.mu.Unlock()
				return nil, err
			}
			lease.retained = true
		}
		return lease, nil
	}
}

func (b *mediaBridge) release(lease *mediaLease) {
	b.mu.Lock()
	lease.users = 0
	close(lease.busy)
	lease.busy = nil
	b.mu.Unlock()
}

func (b *mediaBridge) request(ctx context.Context, baseDir, method string, raw json.RawMessage) (any, error) {
	var metadata media.MediaInfo
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
			metadata, err = b.api.ImportURL(ctx, params.URL, input, media.ImportLimits{})
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
			metadata, err = b.api.ImportReader(ctx, file, info.Size(), input, media.ImportLimits{})
		default:
			if base64.StdEncoding.DecodedLen(len(*params.Base64)) > hookoutput.MaxBase64Bytes {
				return nil, fmt.Errorf("media.import base64 exceeds 10 MiB decoded limit")
			}
			var data []byte
			data, err = base64.StdEncoding.DecodeString(*params.Base64)
			if err == nil {
				metadata, err = b.api.ImportBytes(ctx, data, input, media.ImportLimits{MaxImportBytes: hookoutput.MaxBase64Bytes})
			}
		}
	case "media.read", "media.export", "media.metadata":
		var params struct {
			MediaID string `json:"media"`
		}
		if err := hookoutput.DecodeJSON(raw, &params); err != nil {
			return nil, err
		}
		metadata, err = b.api.Info(ctx, params.MediaID)
	default:
		return nil, fmt.Errorf("unsupported hook media method %q", method)
	}
	if err != nil {
		return nil, fmt.Errorf("hook media operation failed: %w", err)
	}
	lease, err := b.retain(ctx, metadata.ID)
	if err != nil {
		return nil, err
	}
	defer b.release(lease)
	result := mediaResult{MediaID: metadata.ID, Name: metadata.Name, MIMEType: metadata.MIMEType, Size: metadata.Size, ExpiresAt: lease.expires}
	if method == "media.read" && metadata.Size <= mediaInlineBytes {
		data, _, err := b.api.Read(ctx, metadata.ID)
		if err != nil {
			return nil, err
		}
		result.Base64 = base64.StdEncoding.EncodeToString(data)
	} else if method == "media.export" || method == "media.read" {
		if lease.path == "" {
			if lease.dir == "" {
				lease.dir, err = os.MkdirTemp("", "elbot-hook-media-*")
				if err != nil {
					return nil, err
				}
			}
			path := filepath.Join(lease.dir, "media")
			if _, err := b.api.Export(ctx, metadata.ID, path); err != nil {
				return nil, err
			}
			lease.path = path
		}
		result.Path = lease.path
	}
	return result, nil
}

func (b *mediaBridge) prune(ctx context.Context, now time.Time, closeAll bool) (events.LogResult, error) {
	b.mu.Lock()
	if closeAll {
		b.closed = true
	}
	selected := make(map[string]*mediaLease)
	for id, lease := range b.leases {
		if lease.users != 0 || lease.cleaning || (!closeAll && now.Before(lease.expires)) {
			continue
		}
		lease.cleaning, lease.busy = true, make(chan struct{})
		selected[id] = lease
	}
	b.mu.Unlock()
	var failures []error
	logResult := events.LogResult{ResultStatus: events.ResultSkipped, Content: "no Hook media leases due"}
	for id, lease := range selected {
		if logResult.ResultStatus == events.ResultSkipped {
			logResult = events.LogResult{ResultStatus: events.ResultSucceeded, Content: "Hook media lease release"}
		}
		if lease.retained {
			if err := b.api.RemoveReference(ctx, lease.reference); err != nil {
				failures = append(failures, err)
				if !errors.Is(err, context.Canceled) {
					logResult.ResultStatus = events.ResultFailed
				} else if logResult.ResultStatus != events.ResultFailed {
					logResult.ResultStatus = events.ResultCanceled
				}
				logResult.Content += fmt.Sprintf("\nmedia_id=%s reference_release_error=%v", id, err)
			} else {
				lease.retained = false
				logResult.Content += "\nmedia_id=" + id + " reference_released"
			}
		}
		if lease.dir != "" {
			if err := os.RemoveAll(lease.dir); err != nil {
				failures = append(failures, err)
				logResult.ResultStatus = events.ResultFailed
				logResult.Content += fmt.Sprintf("\nmedia_id=%s directory_release_error=%v", id, err)
			} else {
				lease.dir, lease.path = "", ""
			}
		}
		b.mu.Lock()
		if !lease.retained && lease.dir == "" {
			delete(b.leases, id)
			logResult.Content += "\nmedia_id=" + id + " released"
		}
		lease.cleaning = false
		close(lease.busy)
		lease.busy = nil
		b.mu.Unlock()
	}
	return logResult, errors.Join(failures...)
}
