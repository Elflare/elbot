package elnis

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/elvena"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

func (s *Service) downloadSegments(ctx context.Context, elwispName, eventID string, segments []Segment) (map[string]string, error) {
	if len(segments) == 0 {
		return nil, nil
	}
	dir := filepath.Join(s.sandboxRoot, "elnis", sanitizeName(elwispName), sanitizeName(eventID))
	paths := make(map[string]string, len(segments))
	for i, seg := range segments {
		if seg.Kind == SegmentKindText {
			continue
		}
		if err := validateSegmentURL(seg.URL); err != nil {
			return nil, fmt.Errorf("segment %d: %w", i, err)
		}
		resolvedPath, err := s.resolveSegment(ctx, dir, seg)
		if err != nil {
			return nil, fmt.Errorf("segment %d: %w", i, err)
		}
		paths[elvena.SegmentKey(i, seg)] = resolvedPath
	}
	return paths, nil
}

func validateSegmentURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("url is required for image/file segments")
	}
	if strings.HasPrefix(rawURL, "data:") {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("url host is empty")
	}
	return nil
}

func (s *Service) resolveSegment(ctx context.Context, dir string, seg Segment) (string, error) {
	maxBytes := s.cfg.Segment.MaxFileBytes
	timeout := time.Duration(s.cfg.Segment.DownloadTimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if strings.HasPrefix(seg.URL, "data:") {
		return s.decodeDataURI(dir, seg, maxBytes)
	}
	return s.downloadURL(ctx, dir, seg, maxBytes, timeout)
}

func (s *Service) decodeDataURI(dir string, seg Segment, maxBytes int64) (string, error) {
	raw := strings.TrimSpace(seg.URL)
	if !strings.HasPrefix(raw, "data:") {
		return "", fmt.Errorf("not a data URI")
	}
	commaIdx := strings.Index(raw, ",")
	if commaIdx < 0 {
		return "", fmt.Errorf("invalid data URI: missing comma")
	}
	header := raw[5:commaIdx]
	data := raw[commaIdx+1:]
	if !strings.HasSuffix(header, ";base64") {
		return "", fmt.Errorf("data URI without base64 encoding is not supported")
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("base64 decode failed: %w", err)
	}
	if maxBytes > 0 && int64(len(decoded)) > maxBytes {
		return "", fmt.Errorf("decoded data size %d exceeds max_file_bytes %d", len(decoded), maxBytes)
	}

	ext := filepath.Ext(strings.TrimSpace(seg.Name))
	if ext == "" {
		mediaType := strings.TrimSuffix(header, ";base64")
		if exts, _ := mime.ExtensionsByType(mediaType); len(exts) > 0 {
			ext = exts[0]
		}
	}
	if ext == "" {
		if seg.Kind == SegmentKindImage {
			ext = ".png"
		} else {
			ext = ".bin"
		}
	}

	name := strings.TrimSpace(seg.Name)
	if name == "" {
		name = storage.NewID()
	}
	filename := name + ext
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create segment dir: %w", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, decoded, 0644); err != nil {
		return "", fmt.Errorf("write segment file: %w", err)
	}
	return path, nil
}

func (s *Service) downloadURL(ctx context.Context, dir string, seg Segment, maxBytes int64, timeout time.Duration) (string, error) {
	dlCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	headReq, err := http.NewRequestWithContext(dlCtx, http.MethodHead, seg.URL, nil)
	if err != nil {
		return "", fmt.Errorf("create HEAD request: %w", err)
	}
	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		return "", fmt.Errorf("HEAD request failed: %w", err)
	}
	headResp.Body.Close()
	if headResp.ContentLength > 0 && maxBytes > 0 && headResp.ContentLength > maxBytes {
		return "", fmt.Errorf("file size %d exceeds max_file_bytes %d", headResp.ContentLength, maxBytes)
	}

	getReq, err := http.NewRequestWithContext(dlCtx, http.MethodGet, seg.URL, nil)
	if err != nil {
		return "", fmt.Errorf("create GET request: %w", err)
	}
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return "", fmt.Errorf("GET request failed: %w", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode < 200 || getResp.StatusCode >= 300 {
		return "", fmt.Errorf("download returned HTTP %d", getResp.StatusCode)
	}

	var limitReader io.Reader = getResp.Body
	if maxBytes > 0 {
		limitReader = io.LimitReader(getResp.Body, maxBytes+1)
	}
	data, err := io.ReadAll(limitReader)
	if err != nil {
		return "", fmt.Errorf("read download body: %w", err)
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return "", fmt.Errorf("downloaded size %d exceeds max_file_bytes %d", len(data), maxBytes)
	}

	name := strings.TrimSpace(seg.Name)
	if name == "" {
		name = storage.NewID()
		if seg.Kind == SegmentKindImage {
			name += ".png"
		} else {
			name += ".bin"
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create segment dir: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("write segment file: %w", err)
	}
	return path, nil
}

func segmentsContentText(segments []Segment) string {
	var b strings.Builder
	for _, seg := range segments {
		switch seg.Kind {
		case SegmentKindText:
			b.WriteString(seg.Text)
		case SegmentKindImage:
			label := firstNonEmptyStr(seg.Name, seg.URL)
			if label == "" {
				label = "[图片]"
			}
			b.WriteString(fmt.Sprintf("[图片: %s]", label))
		case SegmentKindFile:
			label := firstNonEmptyStr(seg.Name, seg.URL)
			if label == "" {
				label = "[文件]"
			}
			b.WriteString(fmt.Sprintf("[文件: %s]", label))
		}
	}
	return strings.TrimSpace(b.String())
}

func segmentsLLM(segments []Segment) []llm.MessageSegment {
	var out []llm.MessageSegment
	for _, seg := range segments {
		switch seg.Kind {
		case SegmentKindText:
			out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: seg.Text})
		case SegmentKindImage:
			out = append(out, llm.MessageSegment{Type: llm.SegmentImage, URL: seg.URL, Name: seg.Name})
		case SegmentKindFile:
			out = append(out, llm.MessageSegment{Type: llm.SegmentFile, URL: seg.URL, Name: seg.Name})
		}
	}
	return out
}

func sanitizeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
	if name == "" {
		return "_"
	}
	return name
}
