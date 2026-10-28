package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/storage"
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
