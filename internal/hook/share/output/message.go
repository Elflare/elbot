// 将插件消息描述解析为明确来源；导入及租约由运行时负责。
package output

import (
	"elbot/internal/delivery"
	"elbot/internal/media"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// MessageSegment 是进程 Hook 的消息输入协议。
type MessageSegment struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	URL      string `json:"url,omitempty"`
	Path     string `json:"path,omitempty"`
	Base64   string `json:"base64,omitempty"`
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

func BuildMessageSegments(specs []MessageSegment, baseDir string) ([]delivery.Output, error) {
	segments := make([]delivery.Output, 0, len(specs))
	for i, spec := range specs {
		segment, err := buildMessageSegment(spec, baseDir)
		if err != nil {
			return nil, fmt.Errorf("message.segments[%d]: %w", i, err)
		}
		segments = append(segments, segment)
	}
	return segments, nil
}

func buildMessageSegment(spec MessageSegment, baseDir string) (delivery.Output, error) {
	spec.Type = strings.TrimSpace(spec.Type)
	spec.URL = strings.TrimSpace(spec.URL)
	spec.Path = strings.TrimSpace(spec.Path)
	spec.Base64 = strings.TrimSpace(spec.Base64)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.MIMEType = strings.TrimSpace(spec.MIMEType)
	switch spec.Type {
	case "text":
		if spec.URL != "" || spec.Path != "" || spec.Base64 != "" || spec.Name != "" || spec.MIMEType != "" {
			return delivery.Output{}, fmt.Errorf("text segment only supports text")
		}
		return delivery.Text(spec.Text), nil
	case "image":
		return buildMessageImage(spec, baseDir)
	default:
		return delivery.Output{}, fmt.Errorf("unsupported segment type %q", spec.Type)
	}
}

func buildMessageImage(spec MessageSegment, baseDir string) (delivery.Output, error) {
	kind, value := singleMediaSource(spec.Path, spec.URL, spec.Base64)
	if kind == "" {
		return delivery.Output{}, fmt.Errorf("image segment must provide exactly one of url, path or base64")
	}
	segment := delivery.Output{Kind: delivery.KindImage, Text: spec.Text, Name: spec.Name, Source: delivery.Source{MIMEType: spec.MIMEType}}
	if kind == "media_id" {
		if !media.ValidID(value) {
			return delivery.Output{}, fmt.Errorf("invalid media ID")
		}
		segment.Source.MediaID = value
		return segment, nil
	}
	if spec.MIMEType != "" && !strings.HasPrefix(strings.ToLower(spec.MIMEType), "image/") {
		return delivery.Output{}, fmt.Errorf("mime_type must be image/*")
	}
	switch kind {
	case "url":
		u, err := url.Parse(value)
		if err != nil {
			return delivery.Output{}, fmt.Errorf("invalid image URL")
		}
		switch strings.ToLower(u.Scheme) {
		case "data":
			mimeType, data, err := decodeImageDataURL(value)
			if err != nil {
				return delivery.Output{}, err
			}
			if spec.MIMEType != "" && !strings.EqualFold(spec.MIMEType, mimeType) {
				return delivery.Output{}, fmt.Errorf("mime_type does not match data URL")
			}
			segment.Source.MIMEType, segment.Source.Data = mimeType, data
		case "http", "https":
			if u.Host == "" {
				return delivery.Output{}, fmt.Errorf("image URL requires a host")
			}
			u.Scheme = strings.ToLower(u.Scheme)
			segment.Source.URL = u.String()
		default:
			return delivery.Output{}, fmt.Errorf("url must be an absolute HTTP(S) URL or image data URL")
		}
	case "path":
		if strings.Contains(value, "://") {
			return delivery.Output{}, fmt.Errorf("path must be a filesystem path, not a URI")
		}
		if !filepath.IsAbs(value) && baseDir != "" {
			value = filepath.Join(baseDir, value)
		}
		segment.Source.Path = value
	case "base64":
		data, err := decodeBase64(value, "base64 image", "")
		segment.Source.Data = data
		return segment, err
	}
	return segment, nil
}

func decodeImageDataURL(value string) (string, []byte, error) {
	header, encoded, ok := strings.Cut(value, ",")
	lower := strings.ToLower(header)
	if !ok || !strings.HasPrefix(lower, "data:image/") || !strings.HasSuffix(lower, ";base64") {
		return "", nil, fmt.Errorf("image data URL must use base64 encoding")
	}
	mimeType := header[len("data:") : len(header)-len(";base64")]
	data, err := decodeBase64(encoded, "image data URL", "")
	return mimeType, data, err
}
