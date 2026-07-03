package qqonebot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/platform"
)

type savedAttachment struct {
	URL      string
	Path     string
	Name     string
	MIMEType string
}

type inboundAttachments struct {
	Segments []platform.MessageSegment
	Saved    []savedAttachment
	TooLarge []platform.MessageSegment
}

var errAttachmentTooLarge = errors.New("attachment too large")

func (a *Adapter) prepareInboundAttachments(ctx context.Context, segments []platform.MessageSegment) inboundAttachments {
	if len(segments) == 0 {
		return inboundAttachments{}
	}
	out := inboundAttachments{Segments: append([]platform.MessageSegment(nil), segments...)}
	for i := range out.Segments {
		segment := out.Segments[i]
		if segment.Type != platform.SegmentFile || !isDownloadableURL(segment.URL) {
			continue
		}
		saved, err := a.downloadInboundAttachment(ctx, i+1, segment)
		if err != nil {
			if errors.Is(err, errAttachmentTooLarge) {
				out.TooLarge = append(out.TooLarge, segment)
				out.Segments[i] = platform.MessageSegment{}
			} else {
				a.logWarn("download onebot attachment failed", "url", segment.URL, "error", err)
			}
			continue
		}
		out.Saved = append(out.Saved, saved)
		out.Segments[i].URL = saved.URL
		out.Segments[i].MIMEType = saved.MIMEType
		out.Segments[i].Name = saved.Path
	}
	out.Segments = compactMessageSegments(out.Segments)
	return out
}

func compactMessageSegments(segments []platform.MessageSegment) []platform.MessageSegment {
	out := segments[:0]
	for _, segment := range segments {
		if segment.Type != "" {
			out = append(out, segment)
		}
	}
	return out
}

func (a *Adapter) downloadInboundAttachment(ctx context.Context, index int, segment platform.MessageSegment) (savedAttachment, error) {
	attachmentDir := strings.TrimSpace(a.cfg.AttachmentDir)
	if attachmentDir == "" {
		return savedAttachment{}, fmt.Errorf("attachment dir is not configured")
	}
	absAttachmentDir, err := filepath.Abs(attachmentDir)
	if err != nil {
		return savedAttachment{}, fmt.Errorf("resolve attachment dir: %w", err)
	}

	data, header, err := a.downloadInboundAttachmentData(ctx, segment.URL)
	if err != nil {
		return savedAttachment{}, err
	}
	name := inboundAttachmentName(segment, header, index)
	if err := os.MkdirAll(absAttachmentDir, 0o755); err != nil {
		return savedAttachment{}, fmt.Errorf("create attachment dir: %w", err)
	}
	path := uniquePath(filepath.Join(absAttachmentDir, name))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return savedAttachment{}, fmt.Errorf("create attachment file: %w", err)
	}
	defer file.Close()
	written, err := file.Write(data)
	if err != nil {
		return savedAttachment{}, fmt.Errorf("write attachment file: %w", err)
	}
	if written != len(data) {
		_ = file.Close()
		_ = os.Remove(path)
		return savedAttachment{}, fmt.Errorf("short write attachment")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return savedAttachment{}, err
	}
	return savedAttachment{URL: strings.TrimSpace(segment.URL), Path: absPath, Name: filepath.Base(absPath), MIMEType: attachmentMIMEType(segment, header, data)}, nil
}

func (a *Adapter) downloadInboundAttachmentData(ctx context.Context, urlValue string) ([]byte, http.Header, error) {
	if a.cfg.DownloadTimeoutSecs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.cfg.DownloadTimeoutSecs)*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(urlValue), nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("download attachment http %d", resp.StatusCode)
	}
	maxBytes := a.cfg.MaxReceiveFileBytes
	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, fmt.Errorf("read attachment: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, nil, fmt.Errorf("%w: exceeds %d bytes", errAttachmentTooLarge, maxBytes)
	}
	return data, resp.Header.Clone(), nil
}

func inboundAttachmentName(segment platform.MessageSegment, header http.Header, index int) string {
	name := firstNonEmpty(segment.Name, contentDispositionFilename(header.Get("Content-Disposition")))
	if name == "" {
		if parsed, err := url.Parse(segment.URL); err == nil {
			name = filepath.Base(parsed.Path)
		}
	}
	name = sanitizeFilename(name)
	if name == "" || looksLikeQQDownloadName(name) {
		name = fmt.Sprintf("attachment-%d%s", index, extensionFromContentType(header.Get("Content-Type")))
	}
	return name
}

func contentDispositionFilename(value string) string {
	_, params, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	return firstNonEmpty(params["filename*"], params["filename"])
}

func attachmentMIMEType(segment platform.MessageSegment, header http.Header, data []byte) string {
	mimeType := strings.TrimSpace(segment.MIMEType)
	if mimeType == "" {
		mimeType = strings.TrimSpace(header.Get("Content-Type"))
	}
	if mediaType, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = mediaType
	}
	if mimeType == "" || strings.EqualFold(mimeType, "application/octet-stream") {
		mimeType = http.DetectContentType(data)
	}
	return strings.ToLower(mimeType)
}

func isDownloadableURL(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

func platformSavedAttachmentsOutput(attachments []savedAttachment) delivery.Output {
	var sb strings.Builder
	for _, attachment := range attachments {
		if attachment.Path == "" {
			continue
		}
		name := attachment.Name
		if name == "" {
			name = attachment.Path
		}
		sb.WriteString(fmt.Sprintf("已保存附件：%s\n路径：%s\n", name, attachment.Path))
	}
	return delivery.Text(sb.String())
}

func platformTooLargeAttachmentsOutput(segments []platform.MessageSegment, maxBytes int64) delivery.Output {
	var sb strings.Builder
	for _, segment := range segments {
		name := strings.TrimSpace(segment.Name)
		if name == "" {
			name = strings.TrimSpace(segment.Text)
		}
		if name == "" {
			name = "附件"
		}
		sb.WriteString(fmt.Sprintf("文件过大，不会保存到服务器：%s（上限 %d 字节）\n", name, maxBytes))
	}
	return delivery.Text(sb.String())
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, "*", "_")
	name = strings.ReplaceAll(name, "?", "_")
	name = strings.ReplaceAll(name, "\"", "_")
	name = strings.ReplaceAll(name, "<", "_")
	name = strings.ReplaceAll(name, ">", "_")
	name = strings.ReplaceAll(name, "|", "_")
	if len([]rune(name)) > 160 {
		runes := []rune(name)
		name = string(runes[:160])
	}
	return strings.Trim(name, " .")
}

func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func extensionFromContentType(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		mediaType = strings.TrimSpace(strings.ToLower(value))
	}
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	default:
		return ""
	}
}

func looksLikeQQDownloadName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(lower, "qqdownload") || strings.HasPrefix(lower, "robot1.0_") || len([]rune(lower)) > 96
}
