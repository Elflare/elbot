package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"elbot/internal/config"
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

type requestMedia struct {
	metadata   *storage.Media
	data       []byte
	compressed bool
}

// ResolveForLLM only changes a request copy. The cleanup releases request-scoped remote objects.
func (m *Manager) ResolveForLLM(ctx context.Context, messages []llm.LLMMessage) ([]llm.LLMMessage, func(), error) {
	out := llm.CloneMessages(messages)
	cleanup := &cleanupList{}
	mediaByID := map[string]*requestMedia{}
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
			cacheKey := segment.MediaID + "\x00" + string(segment.Type)
			item, ok := mediaByID[cacheKey]
			if !ok {
				metadata, err := m.Metadata(ctx, segment.MediaID)
				if err != nil {
					out[i].Segments[j] = unavailable(segment)
					continue
				}
				item = &requestMedia{metadata: metadata}
				if segment.Type == llm.SegmentImage {
					data, _, err := m.Read(ctx, segment.MediaID)
					if err != nil {
						out[i].Segments[j] = unavailable(segment)
						continue
					}
					item.data = data
					if shouldCompressImage(metadata, data, m.Media) {
						compressed, err := compressImage(data, m.Media.LLMImageCompressionThresholdBytes, m.Media.LLMImageMaxLength)
						if err != nil {
							out[i].Segments[j] = unavailable(segment)
							continue
						}
						item.data = compressed
						item.compressed = true
						item.metadata = compressedMetadata(metadata, len(compressed))
					}
				}
				mediaByID[cacheKey] = item
			}
			if item.metadata == nil {
				continue
			}
			if item.compressed {
				out[i].Segments[j].Name = item.metadata.Name
				out[i].Segments[j].MIMEType = item.metadata.MIMEType
			} else if out[i].Segments[j].Name == "" {
				out[i].Segments[j].Name = item.metadata.Name
			}
			total += itemSize(item)
		}
	}

	remote := m.FileDelivery.Backend == "s3" || (m.FileDelivery.Backend == "hybrid" && total > m.FileDelivery.MaxDirectBase64Bytes)
	if !remote && total > m.FileDelivery.MaxDirectBase64Bytes {
		return nil, func() {}, fmt.Errorf("media request is %d bytes, exceeds file_delivery.max_direct_base64_bytes=%d", total, m.FileDelivery.MaxDirectBase64Bytes)
	}
	urls := map[string]string{}
	for i := range out {
		for j, segment := range out[i].Segments {
			if segment.MediaID == "" {
				continue
			}
			cacheKey := segment.MediaID + "\x00" + string(segment.Type)
			item := mediaByID[cacheKey]
			if item == nil || item.metadata == nil {
				continue
			}
			urlKey := segment.MediaID + "\x00" + string(segment.Type)
			value, ok := urls[urlKey]
			if !ok {
				var err error
				if remote {
					if item.compressed {
						backend, backendErr := m.remoteBackend(ctx)
						if backendErr != nil {
							err = backendErr
						} else if temporary, ok := backend.(temporaryBackend); ok {
							var key string
							key, err = temporary.PutTemporary(ctx, bytes.NewReader(item.data), int64(len(item.data)), item.metadata.MIMEType)
							if err == nil {
								cleanup.add(func() {
									cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
									defer cancel()
									if err := temporary.RemoveTemporary(cleanupCtx, key); err != nil && m.Logger != nil {
										m.Logger.Warn("remove temporary LLM media failed", "key", key, "error", err)
									}
								})
								value, err = temporary.PresignTemporary(ctx, key, time.Hour)
							}
						} else {
							err = fmt.Errorf("remote media backend does not support temporary objects")
						}
					} else {
						value, err = m.PresignGet(ctx, segment.MediaID, time.Hour)
					}
				} else {
					data := item.data
					if len(data) == 0 {
						data, _, err = m.Read(ctx, segment.MediaID)
					}
					if err == nil {
						value = "data:" + item.metadata.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(data)
					}
				}
				if err != nil {
					out[i].Segments[j] = unavailable(segment)
					continue
				}
				urls[urlKey] = value
			}
			out[i].Segments[j].URL = value
		}
	}
	return out, cleanup.run, nil
}

func shouldCompressImage(metadata *storage.Media, data []byte, cfg config.MediaConfig) bool {
	if metadata == nil {
		return false
	}
	if int64(len(data)) > cfg.LLMImageCompressionThresholdBytes {
		return true
	}
	width, height, err := imageDimensions(data)
	return err == nil && (width >= cfg.LLMImageMaxLength || height >= cfg.LLMImageMaxLength)
}

func compressedMetadata(metadata *storage.Media, size int) *storage.Media {
	copy := *metadata
	copy.Name = compressedName(metadata.Name)
	copy.MIMEType = "image/jpeg"
	copy.Size = int64(size)
	return &copy
}

func compressedName(name string) string {
	name = sanitizeMediaName(name)
	ext := filepath.Ext(name)
	if ext == "" {
		return name + ".jpg"
	}
	return strings.TrimSuffix(name, ext) + ".jpg"
}

func itemSize(item *requestMedia) int64 {
	if item.compressed {
		return int64(len(item.data))
	}
	return item.metadata.Size
}

type cleanupList struct {
	mu    sync.Mutex
	items []func()
	done  bool
}

func (c *cleanupList) add(cleanup func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		cleanup()
		return
	}
	c.items = append(c.items, cleanup)
}

func (c *cleanupList) run() {
	c.mu.Lock()
	if c.done {
		c.mu.Unlock()
		return
	}
	c.done = true
	items := append([]func(){}, c.items...)
	c.mu.Unlock()
	for i := len(items) - 1; i >= 0; i-- {
		items[i]()
	}
}
