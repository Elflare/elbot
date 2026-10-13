package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/llm"
	"elbot/internal/storage"

	"github.com/aws/smithy-go"
)

// Materialize replaces new inbound media sources with stable references.
func (m *Manager) Materialize(ctx context.Context, segments []llm.MessageSegment) ([]llm.MessageSegment, error) {
	return m.MaterializeWithLimits(ctx, segments, ImportLimits{})
}

// MaterializeWithLimits materializes segments using per-call import limits
// without changing the shared manager.
func (m *Manager) MaterializeWithLimits(ctx context.Context, segments []llm.MessageSegment, limits ImportLimits) ([]llm.MessageSegment, error) {
	if limits.MaxImportBytes <= 0 || limits.MaxImportBytes > m.MaxImportBytes {
		limits.MaxImportBytes = m.MaxImportBytes
	}
	if limits.DownloadTimeout <= 0 {
		limits.DownloadTimeout = m.DownloadTimeout
	}
	out := append([]llm.MessageSegment(nil), segments...)
	for i, segment := range out {
		if segment.Type != llm.SegmentImage && segment.Type != llm.SegmentFile {
			continue
		}
		if segment.MediaID != "" {
			out[i].URL = ""
			continue
		}
		if segment.Name != "" {
			segment.Name = sanitizeMediaName(segment.Name)
		}
		var metadata *storage.Media
		var err error
		input := Input{Name: segment.Name, MIMEType: segment.MIMEType}
		switch {
		case strings.HasPrefix(segment.URL, "data:"):
			header, body, ok := strings.Cut(segment.URL, ",")
			if !ok || !strings.HasSuffix(header, ";base64") {
				err = fmt.Errorf("invalid media data URL")
				break
			}
			if int64(len(body)) > (limits.MaxImportBytes+2)/3*4 {
				err = fmt.Errorf("media exceeds import limit")
				break
			}
			var data []byte
			data, err = base64.StdEncoding.DecodeString(body)
			if err == nil {
				if input.MIMEType == "" {
					input.MIMEType = strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
				}
				metadata, err = m.importReader(ctx, bytes.NewReader(data), int64(len(data)), input, limits.MaxImportBytes)
			}
		case strings.HasPrefix(segment.URL, "http://") || strings.HasPrefix(segment.URL, "https://"):
			metadata, err = m.importURL(ctx, segment.URL, input, limits)
		case segment.URL != "":
			path := segment.URL
			if strings.HasPrefix(path, "file://") {
				var u *url.URL
				u, err = url.Parse(path)
				if err != nil {
					break
				}
				path = u.Path
				if len(path) > 2 && path[0] == '/' && path[2] == ':' {
					path = path[1:]
				}
				if u.Host != "" && u.Host != "localhost" {
					err = fmt.Errorf("unsupported file URL host")
					break
				}
			}
			metadata, err = m.importFile(ctx, filepath.FromSlash(path), input, limits.MaxImportBytes)
		default:
			err = fmt.Errorf("media source is empty")
		}
		if err != nil {
			return nil, fmt.Errorf("archive media segment %d: %w", i, err)
		}
		if segment.MediaID == "" {
			segment.Name = metadata.Name
			segment.MIMEType = metadata.MIMEType
		}
		segment.MediaID, segment.URL = metadata.ID, ""
		if segment.Name == "" {
			segment.Name = metadata.Name
		}
		if segment.MIMEType == "" {
			segment.MIMEType = metadata.MIMEType
		}
		out[i] = segment
	}
	return out, ctx.Err()
}

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

// ResolveForLLM only changes a request copy and uses media as stored.
func (m *Manager) ResolveForLLM(ctx context.Context, messages []llm.LLMMessage) ([]llm.LLMMessage, func(), error) {
	out := llm.CloneMessages(messages)
	cleanup := func() {}
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
					value, err = m.PresignGet(ctx, segment.MediaID, time.Hour)
				} else {
					var data []byte
					data, _, err = m.Read(ctx, segment.MediaID)
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
					missing := errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, storage.ErrNotFound) || (errors.As(err, &remoteErr) && (remoteErr.ErrorCode() == "NoSuchKey" || remoteErr.ErrorCode() == "NotFound"))
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
