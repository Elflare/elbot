package media

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/llm"
	"elbot/internal/storage"
)

// Materialize replaces new inbound media sources with stable references.
func (m *Manager) Materialize(ctx context.Context, segments []llm.MessageSegment) []llm.MessageSegment {
	out := append([]llm.MessageSegment(nil), segments...)
	for i, segment := range out {
		if segment.Type != llm.SegmentImage && segment.Type != llm.SegmentFile {
			continue
		}
		if segment.Name != "" {
			segment.Name = sanitizeMediaName(segment.Name)
		}
		var metadata *storage.Media
		var err error
		input := Input{Name: segment.Name, MIMEType: segment.MIMEType}
		switch {
		case segment.MediaID != "":
			if !ValidID(segment.MediaID) {
				err = fmt.Errorf("invalid media ID")
			} else {
				metadata, err = m.Metadata(ctx, segment.MediaID)
			}
		case strings.HasPrefix(segment.URL, "data:"):
			header, body, ok := strings.Cut(segment.URL, ",")
			if !ok || !strings.HasSuffix(header, ";base64") {
				err = fmt.Errorf("invalid media data URL")
				break
			}
			if int64(len(body)) > (m.MaxImportBytes+2)/3*4 {
				err = fmt.Errorf("media exceeds import limit")
				break
			}
			var data []byte
			data, err = base64.StdEncoding.DecodeString(body)
			if err == nil {
				if input.MIMEType == "" {
					input.MIMEType = strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
				}
				metadata, err = m.ImportBytes(ctx, data, input)
			}
		case strings.HasPrefix(segment.URL, "http://") || strings.HasPrefix(segment.URL, "https://"):
			metadata, err = m.ImportURL(ctx, segment.URL, input)
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
			metadata, err = m.ImportFile(ctx, filepath.FromSlash(path), input)
		default:
			continue
		}
		if err != nil {
			out[i] = unavailable(segment)
			continue
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
	return out
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

// ResolveForLLM only changes a request copy. The threshold covers the entire request.
func (m *Manager) ResolveForLLM(ctx context.Context, messages []llm.LLMMessage) ([]llm.LLMMessage, error) {
	out := llm.CloneMessages(messages)
	metadata := map[string]*storage.Media{}
	var total int64
	for i := range out {
		for j := range out[i].Segments {
			segment := out[i].Segments[j]
			if (segment.Type == llm.SegmentImage || segment.Type == llm.SegmentFile) && segment.Name != "" {
				segment.Name = sanitizeMediaName(segment.Name)
				out[i].Segments[j].Name = segment.Name
			}
			if segment.MediaID == "" {
				continue
			}
			item, err := m.Metadata(ctx, segment.MediaID)
			if err != nil {
				out[i].Segments[j] = unavailable(segment)
				continue
			}
			if out[i].Segments[j].Name == "" {
				out[i].Segments[j].Name = item.Name
			}
			metadata[segment.MediaID] = item
			total += item.Size
		}
	}
	remote := m.FileDelivery.Backend == "s3" || (m.FileDelivery.Backend == "hybrid" && total > m.FileDelivery.MaxDirectBase64Bytes)
	if !remote && total > m.FileDelivery.MaxDirectBase64Bytes {
		return nil, fmt.Errorf("media request is %d bytes, exceeds file_delivery.max_direct_base64_bytes=%d", total, m.FileDelivery.MaxDirectBase64Bytes)
	}
	urls := map[string]string{}
	for i := range out {
		for j, segment := range out[i].Segments {
			if segment.MediaID == "" {
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
						value = "data:" + metadata[segment.MediaID].MIMEType + ";base64," + base64.StdEncoding.EncodeToString(data)
					}
				}
				if err != nil {
					out[i].Segments[j] = unavailable(segment)
					continue
				}
				urls[segment.MediaID] = value
			}
			out[i].Segments[j].URL = value
		}
	}
	return out, nil
}
