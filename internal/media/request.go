package media

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"elbot/internal/llm"
	"elbot/internal/storage"
	"github.com/aws/smithy-go"
)

const signedURLRefreshMargin = 5 * time.Minute

func unavailable(segment llm.MessageSegment) llm.MessageSegment {
	label := strings.TrimSpace(segment.Name)
	if label != "" {
		label = sanitizeMediaName(label)
	}
	if segment.MediaID != "" {
		label += "；媒体 ID：" + segment.MediaID
	}
	return llm.MessageSegment{Type: llm.SegmentText, Text: "[媒体不可用；" + label + "]"}
}

// AcquireForLLM prepares a request copy and holds its media until release.
func (m *Manager) AcquireForLLM(ctx context.Context, messages []llm.LLMMessage) (_ []llm.LLMMessage, cleanup func(), err error) {
	out := llm.CloneMessages(messages)
	var releases []func() error
	cleanup = func() {
		for i := len(releases) - 1; i >= 0; i-- {
			_ = releases[i]()
		}
		releases = nil
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	mediaByID := map[string]*storage.Media{}
	var total int64
	for i := range out {
		for j, segment := range out[i].Segments {
			if (segment.Type == llm.SegmentImage || segment.Type == llm.SegmentFile) && segment.Name != "" {
				out[i].Segments[j].Name = sanitizeMediaName(segment.Name)
			}
			if segment.MediaID == "" {
				continue
			}
			metadata, ok := mediaByID[segment.MediaID]
			if !ok {
				var err error
				metadata, err = m.Metadata(ctx, segment.MediaID)
				if err != nil {
					if !errors.Is(err, storage.ErrNotFound) {
						return nil, cleanup, err
					}
				}
				if metadata != nil {
					release, err := m.holdMedia(ctx, metadata)
					if err != nil {
						return nil, cleanup, err
					}
					releases = append(releases, release)
				}
				mediaByID[segment.MediaID] = metadata
			}
			if metadata == nil {
				out[i].Segments[j] = unavailable(segment)
				continue
			}
			if out[i].Segments[j].Name == "" {
				out[i].Segments[j].Name = metadata.Name
			}
			out[i].Segments[j].MIMEType = metadata.MIMEType
			total += metadata.Size
		}
	}
	remote := m.FileDelivery.Backend == "s3" || (m.FileDelivery.Backend == "hybrid" && total > m.FileDelivery.MaxDirectBase64Bytes)
	if !remote && total > m.FileDelivery.MaxDirectBase64Bytes {
		return nil, cleanup, fmt.Errorf("media request is %d bytes, exceeds file_delivery.max_direct_base64_bytes=%d", total, m.FileDelivery.MaxDirectBase64Bytes)
	}
	urls := map[string]string{}
	for i := range out {
		for j, segment := range out[i].Segments {
			metadata := mediaByID[segment.MediaID]
			if segment.MediaID == "" || metadata == nil {
				continue
			}
			value, ok := urls[segment.MediaID]
			if !ok {
				var err error
				if remote {
					value, err = m.presignMedia(ctx, metadata)
				} else {
					var data []byte
					data, err = m.readMedia(ctx, metadata)
					if err == nil {
						value = "data:" + metadata.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(data)
					}
				}
				if err != nil {
					if ctx.Err() != nil {
						return nil, cleanup, ctx.Err()
					}
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return nil, cleanup, err
					}
					var remoteErr smithy.APIError
					missing := errors.Is(err, fs.ErrNotExist) || errors.Is(err, storage.ErrNotFound) || (errors.As(err, &remoteErr) && (remoteErr.ErrorCode() == "NoSuchKey" || remoteErr.ErrorCode() == "NotFound"))
					if !missing {
						return nil, cleanup, err
					}
					value = ""
				}
				urls[segment.MediaID] = value
			}
			if value == "" {
				out[i].Segments[j] = unavailable(segment)
				continue
			}
			out[i].Segments[j].URL = value
		}
	}
	return out, cleanup, nil
}

func (m *Manager) readMedia(ctx context.Context, metadata *storage.Media) ([]byte, error) {
	reader, err := m.openMedia(ctx, metadata)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read media %q: %w", metadata.ID, err)
	}
	return data, nil
}

func (m *Manager) presignMedia(ctx context.Context, metadata *storage.Media) (string, error) {
	id := metadata.ID
	unlock, err := m.objects.acquire(ctx, id)
	if err != nil {
		return "", err
	}
	defer unlock()
	// A request may have read the local record before another request uploaded it.
	if metadata.ObjectKey == "" && metadata.Backend != "s3" {
		current, err := m.Store.Media().Get(ctx, id)
		if err != nil {
			return "", err
		}
		*metadata = *current
	}
	backend, err := m.remoteBackend(ctx)
	if err != nil {
		return "", err
	}
	// Stored media may still be local after the configured backend switches to S3.
	if metadata.ObjectKey == "" && metadata.Backend != "s3" {
		reader, err := m.openMedia(ctx, metadata)
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
	cached, err := m.Store.Media().GetSignedURL(ctx, id)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", err
	}
	now := m.Now()
	if err == nil && cached.ExpiresAt.After(now.Add(signedURLRefreshMargin)) {
		return cached.URL, nil
	}
	// Start before signing and round down to avoid overstating the URL lifetime.
	expiresAt := now.Truncate(time.Second).Add(time.Hour)
	url, err := backend.PresignGet(ctx, metadata, time.Hour)
	if err != nil {
		return "", err
	}
	if err := m.Store.Media().UpsertSignedURL(ctx, &storage.MediaSignedURL{MediaID: id, URL: url, ExpiresAt: expiresAt}); err != nil {
		return "", err
	}
	return url, nil
}
