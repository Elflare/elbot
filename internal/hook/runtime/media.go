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
const mediaCleanupRetries = 3 // 首次清理之外的自动重试上限，间隔依次为 1、2、4 分钟。

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
	reference       media.Reference // 租约对应的持久引用身份。
	expires         time.Time       // 最后一次使用后的到期时间，由集合锁保护。
	dir             string          // 租约拥有的临时目录，由占用者独占访问。
	path            string          // 已完成导出的路径，由占用者独占访问。
	retained        bool            // 引用已取得且尚未成功释放，避免重复清理成功项。
	busy            chan struct{}   // 当前独占操作结束时关闭；等待者可取消。
	cleanupFailures int             // 本次租约周期的清理失败次数，由集合锁保护；成功续用后归零。
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
	expiry expiryQueue   // 到期与失败重试共用的索引，由 mu 保护。
	wake   chan struct{} // 最近期限变化通知，可合并，由维护循环消费。
}

func newMediaBridge(api mediaAPI) *mediaBridge {
	return &mediaBridge{api: api, owner: randomID("hook-media"), leases: map[string]*mediaLease{}, wake: make(chan struct{}, 1)}
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
		lease.busy = make(chan struct{})
		expires := time.Now().Add(mediaLeaseTTL)
		deadline := lease.expires
		if entry := b.expiry.byKey[id]; entry != nil {
			deadline = entry.deadline
		}
		b.mu.Unlock()
		if !lease.retained {
			if err := b.api.AddReference(ctx, lease.reference); err != nil {
				b.mu.Lock()
				if lease.dir == "" {
					delete(b.leases, id)
					if b.expiry.remove(id) {
						b.wakeExpiry()
					}
				} else if lease.cleanupFailures <= mediaCleanupRetries {
					b.scheduleExpiryLocked(id, deadline)
				}
				close(lease.busy)
				lease.busy = nil
				b.mu.Unlock()
				return nil, err
			}
			lease.retained = true
		}
		b.mu.Lock()
		lease.expires = expires
		lease.cleanupFailures = 0
		b.scheduleExpiryLocked(id, expires)
		b.mu.Unlock()
		return lease, nil
	}
}

func (b *mediaBridge) release(lease *mediaLease) {
	b.mu.Lock()
	close(lease.busy)
	lease.busy = nil
	if !time.Now().Before(lease.expires) && lease.cleanupFailures <= mediaCleanupRetries {
		b.scheduleExpiryLocked(lease.reference.MediaID, lease.expires)
	}
	b.mu.Unlock()
}

func (b *mediaBridge) scheduleExpiryLocked(id string, deadline time.Time) {
	if b.expiry.set(id, deadline) {
		b.wakeExpiry()
	}
}

func (b *mediaBridge) wakeExpiry() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
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

func (b *mediaBridge) prune(ctx context.Context, now time.Time, closeAll bool, log func(events.LogResult)) error {
	// Only shutdown visits the entire collection. Snapshot keys so a failed
	// lease is attempted once per Close, without occupying the whole batch.
	var keys []string
	if closeAll {
		b.mu.Lock()
		for id := range b.leases {
			keys = append(keys, id)
		}
		b.mu.Unlock()
	}
	var failures []error
	for {
		if !closeAll && ctx.Err() != nil {
			break
		}
		b.mu.Lock()
		var id string
		if closeAll {
			if len(keys) == 0 {
				b.mu.Unlock()
				break
			}
			id, keys = keys[len(keys)-1], keys[:len(keys)-1]
			if b.expiry.remove(id) {
				b.wakeExpiry()
			}
		} else {
			var ok bool
			id, ok = b.expiry.popDue(now)
			if !ok {
				b.mu.Unlock()
				break
			}
			b.wakeExpiry()
		}
		lease := b.leases[id]
		if lease.busy != nil {
			b.mu.Unlock()
			continue // release will schedule this lease if it remains expired.
		}
		lease.busy = make(chan struct{})
		b.mu.Unlock()
		result, err := b.pruneLease(ctx, id, lease, closeAll)
		log(result)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// pruneLease holds this lease's busy slot throughout I/O and final state changes.
func (b *mediaBridge) pruneLease(ctx context.Context, id string, lease *mediaLease, closeAll bool) (events.LogResult, error) {
	var failures []error
	logResult := events.LogResult{ResultStatus: events.ResultSucceeded, Content: "Hook media lease release\nmedia_id=" + id}
	if lease.retained {
		if err := b.api.RemoveReference(ctx, lease.reference); err != nil {
			failures = append(failures, err)
			logResult.ResultStatus = events.ResultFailed
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				logResult.ResultStatus = events.ResultCanceled
			}
			logResult.Content += fmt.Sprintf("\nreference_release_error=%v", err)
		} else {
			lease.retained = false
			logResult.Content += "\nreference_released"
		}
	}
	if lease.dir != "" {
		if err := os.RemoveAll(lease.dir); err != nil {
			failures = append(failures, err)
			logResult.ResultStatus = events.ResultFailed
			logResult.Content += fmt.Sprintf("\ndirectory_release_error=%v", err)
		} else {
			lease.dir, lease.path = "", ""
		}
	}
	b.mu.Lock()
	if !lease.retained && lease.dir == "" {
		delete(b.leases, id)
		logResult.Content += "\nreleased"
	} else if closeAll {
		logResult.Content += "\ncleanup_mode=shutdown"
	} else if ctx.Err() == nil {
		lease.cleanupFailures++
		logResult.Content += fmt.Sprintf("\nattempt=%d retry_limit=%d", lease.cleanupFailures, mediaCleanupRetries)
		if lease.cleanupFailures <= mediaCleanupRetries {
			next := time.Now().Add(time.Minute << (lease.cleanupFailures - 1))
			b.scheduleExpiryLocked(id, next)
			logResult.Content += "\nnext_retry_at=" + next.Format(time.RFC3339Nano)
		} else {
			logResult.Content += "\nretry_exhausted=true"
		}
	} else {
		logResult.Content += "\nretry_stopped=runtime_canceled"
	}
	close(lease.busy)
	lease.busy = nil
	b.mu.Unlock()
	return logResult, errors.Join(failures...)
}
