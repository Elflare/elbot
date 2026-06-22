package qqofficial

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

	"elbot/internal/platform"
)

const maxInboundAttachmentBytes = 50 << 20

type savedAttachment struct {
	URL      string
	Path     string
	Name     string
	MIMEType string
}

type inboundAttachments struct {
	Segments []platform.MessageSegment
	Saved    []savedAttachment
}

func (a *Adapter) prepareInboundAttachments(ctx context.Context, attachments []messageAttachment) inboundAttachments {
	if len(attachments) == 0 {
		return inboundAttachments{}
	}
	var out inboundAttachments
	for i, attachment := range attachments {
		urlValue := strings.TrimSpace(attachment.URL)
		if urlValue == "" {
			continue
		}
		if isImageAttachment(attachment) {
			segment, err := a.downloadInboundImageSegment(ctx, i+1, attachment)
			if err != nil {
				a.logWarn(ctx, "download qqofficial image failed", "url", urlValue, "error", err)
				continue
			}
			out.Segments = append(out.Segments, segment)
			continue
		}
		saved, err := a.downloadInboundAttachment(ctx, i+1, attachment)
		if err != nil {
			a.logWarn(ctx, "download qqofficial attachment failed", "url", urlValue, "error", err)
			continue
		}
		out.Saved = append(out.Saved, saved)
		out.Segments = append(out.Segments, platform.MessageSegment{Type: platform.SegmentFile, Text: "文件", URL: saved.URL, MIMEType: saved.MIMEType, Name: saved.Path})
	}
	return out
}

func (a *Adapter) downloadInboundImageSegment(ctx context.Context, index int, attachment messageAttachment) (platform.MessageSegment, error) {
	data, header, err := a.downloadInboundAttachmentData(ctx, attachment.URL)
	if err != nil {
		return platform.MessageSegment{}, err
	}
	mimeType := attachmentMIMEType(attachment, header, data)
	name := inboundAttachmentName(attachment, header, index)
	return platform.MessageSegment{Type: platform.SegmentImage, URL: dataURL(data, mimeType), MIMEType: mimeType, Name: name}, nil
}

func (a *Adapter) downloadInboundAttachment(ctx context.Context, index int, attachment messageAttachment) (savedAttachment, error) {
	urlValue := strings.TrimSpace(attachment.URL)
	attachmentDir := strings.TrimSpace(a.cfg.AttachmentDir)
	if attachmentDir == "" {
		return savedAttachment{}, fmt.Errorf("attachment dir is not configured")
	}
	absAttachmentDir, err := filepath.Abs(attachmentDir)
	if err != nil {
		return savedAttachment{}, fmt.Errorf("resolve attachment dir: %w", err)
	}

	data, header, err := a.downloadInboundAttachmentData(ctx, urlValue)
	if err != nil {
		return savedAttachment{}, err
	}
	name := inboundAttachmentName(attachment, header, index)
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
	return savedAttachment{URL: urlValue, Path: absPath, Name: filepath.Base(absPath), MIMEType: attachmentMIMEType(attachment, header, data)}, nil
}

func (a *Adapter) downloadInboundAttachmentData(ctx context.Context, urlValue string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(urlValue), nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := a.client.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("download attachment http %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxInboundAttachmentBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, fmt.Errorf("read attachment: %w", err)
	}
	if len(data) > maxInboundAttachmentBytes {
		return nil, nil, fmt.Errorf("attachment exceeds %d bytes", maxInboundAttachmentBytes)
	}
	return data, resp.Header.Clone(), nil
}

func inboundAttachmentName(attachment messageAttachment, header http.Header, index int) string {
	name := firstNonEmpty(attachment.Filename, contentDispositionFilename(header.Get("Content-Disposition")))
	if name == "" {
		if parsed, err := url.Parse(attachment.URL); err == nil {
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

func isImageAttachment(attachment messageAttachment) bool {
	contentType := strings.ToLower(strings.TrimSpace(attachment.ContentType))
	if strings.HasPrefix(contentType, "image/") {
		return true
	}
	if contentType == "file" {
		return false
	}
	if attachment.Width > 0 || attachment.Height > 0 {
		return true
	}
	return isImageURL(attachment.Filename) || isImageURL(attachment.URL)
}

func attachmentMIMEType(attachment messageAttachment, header http.Header, data []byte) string {
	mimeType := strings.TrimSpace(attachment.ContentType)
	if mimeType == "" || strings.EqualFold(mimeType, "file") {
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

func dataURL(data []byte, mimeType string) string {
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
