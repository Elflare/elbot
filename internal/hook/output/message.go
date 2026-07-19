package output

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/llm"
)

// MessageSegment is the process Hook wire format for replacing message content.
type MessageSegment struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	URL      string `json:"url,omitempty"`
	Path     string `json:"path,omitempty"`
	Base64   string `json:"base64,omitempty"`
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

func BuildMessageSegments(specs []MessageSegment, baseDir string) ([]llm.MessageSegment, error) {
	segments := make([]llm.MessageSegment, 0, len(specs))
	for i, spec := range specs {
		segment, err := buildMessageSegment(spec, baseDir)
		if err != nil {
			return nil, fmt.Errorf("message.segments[%d]: %w", i, err)
		}
		segments = append(segments, segment)
	}
	return segments, nil
}

func buildMessageSegment(spec MessageSegment, baseDir string) (llm.MessageSegment, error) {
	spec.Type = strings.TrimSpace(spec.Type)
	spec.URL = strings.TrimSpace(spec.URL)
	spec.Path = strings.TrimSpace(spec.Path)
	spec.Base64 = strings.TrimSpace(spec.Base64)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.MIMEType = strings.TrimSpace(spec.MIMEType)
	switch llm.MessageSegmentType(spec.Type) {
	case llm.SegmentText:
		if spec.URL != "" || spec.Path != "" || spec.Base64 != "" || spec.Name != "" || spec.MIMEType != "" {
			return llm.MessageSegment{}, fmt.Errorf("text segment only supports text")
		}
		return llm.MessageSegment{Type: llm.SegmentText, Text: spec.Text}, nil
	case llm.SegmentImage:
		return buildMessageImage(spec, baseDir)
	default:
		return llm.MessageSegment{}, fmt.Errorf("unsupported segment type %q", spec.Type)
	}
}

func buildMessageImage(spec MessageSegment, baseDir string) (llm.MessageSegment, error) {
	sourceCount := 0
	for _, value := range []string{spec.URL, spec.Path, spec.Base64} {
		if value != "" {
			sourceCount++
		}
	}
	if sourceCount != 1 {
		return llm.MessageSegment{}, fmt.Errorf("image segment must provide exactly one of url, path or base64")
	}
	segment := llm.MessageSegment{Type: llm.SegmentImage, Text: spec.Text, Name: spec.Name, MIMEType: spec.MIMEType}
	if spec.URL != "" {
		if strings.HasPrefix(strings.ToLower(spec.URL), "data:") {
			mimeType, data, err := decodeImageDataURL(spec.URL)
			if err != nil {
				return llm.MessageSegment{}, err
			}
			if segment.MIMEType != "" && !strings.EqualFold(segment.MIMEType, mimeType) {
				return llm.MessageSegment{}, fmt.Errorf("mime_type %q does not match data URL %q", segment.MIMEType, mimeType)
			}
			segment.MIMEType = mimeType
			segment.URL = imageDataURL(mimeType, data)
			return segment, nil
		}
		u, err := url.Parse(spec.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return llm.MessageSegment{}, fmt.Errorf("url must be an absolute HTTP(S) URL or image data URL")
		}
		if segment.MIMEType != "" && !strings.HasPrefix(strings.ToLower(segment.MIMEType), "image/") {
			return llm.MessageSegment{}, fmt.Errorf("mime_type must be image/*")
		}
		segment.URL = spec.URL
		return segment, nil
	}

	var data []byte
	var err error
	if spec.Path != "" {
		path := spec.Path
		if strings.Contains(path, "://") {
			return llm.MessageSegment{}, fmt.Errorf("path must be a filesystem path, not a URI")
		}
		if !filepath.IsAbs(path) && strings.TrimSpace(baseDir) != "" {
			path = filepath.Join(baseDir, path)
		}
		data, err = readLimitedImage(path)
		if err != nil {
			return llm.MessageSegment{}, err
		}
		if segment.Name == "" {
			segment.Name = filepath.Base(path)
		}
		if segment.MIMEType == "" {
			segment.MIMEType = mime.TypeByExtension(filepath.Ext(path))
		}
	} else {
		if base64.StdEncoding.DecodedLen(len(spec.Base64)) > MaxBase64Bytes {
			return llm.MessageSegment{}, fmt.Errorf("base64 image exceeds 10 MiB decoded limit")
		}
		data, err = base64.StdEncoding.DecodeString(spec.Base64)
		if err != nil {
			return llm.MessageSegment{}, fmt.Errorf("decode base64 image: %w", err)
		}
	}
	if len(data) > MaxBase64Bytes {
		return llm.MessageSegment{}, fmt.Errorf("image exceeds 10 MiB decoded limit")
	}
	if segment.MIMEType == "" {
		segment.MIMEType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(strings.ToLower(segment.MIMEType), "image/") {
		return llm.MessageSegment{}, fmt.Errorf("mime_type must be image/*")
	}
	segment.URL = imageDataURL(segment.MIMEType, data)
	return segment, nil
}

func readLimitedImage(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open image path: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxBase64Bytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image path: %w", err)
	}
	if len(data) > MaxBase64Bytes {
		return nil, fmt.Errorf("image path exceeds 10 MiB decoded limit")
	}
	return data, nil
}

func decodeImageDataURL(value string) (string, []byte, error) {
	header, encoded, ok := strings.Cut(value, ",")
	headerLower := strings.ToLower(header)
	if !ok || !strings.HasPrefix(headerLower, "data:image/") || !strings.HasSuffix(headerLower, ";base64") {
		return "", nil, fmt.Errorf("image data URL must use base64 encoding")
	}
	mimeType := header[len("data:") : len(header)-len(";base64")]
	if base64.StdEncoding.DecodedLen(len(encoded)) > MaxBase64Bytes {
		return "", nil, fmt.Errorf("image data URL exceeds 10 MiB decoded limit")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, fmt.Errorf("decode image data URL: %w", err)
	}
	if len(data) > MaxBase64Bytes {
		return "", nil, fmt.Errorf("image data URL exceeds 10 MiB decoded limit")
	}
	return mimeType, data, nil
}

func imageDataURL(mimeType string, data []byte) string {
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
