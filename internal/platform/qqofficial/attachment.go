package qqofficial

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const maxInboundAttachmentBytes = 50 << 20

type savedAttachment struct {
	URL  string
	Path string
	Name string
}

func (a *Adapter) saveInboundAttachments(ctx context.Context, openID, messageID string, attachments []messageAttachment) []savedAttachment {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]savedAttachment, 0, len(attachments))
	for i, attachment := range attachments {
		urlValue := strings.TrimSpace(attachment.URL)
		if urlValue == "" {
			continue
		}
		saved, err := a.downloadInboundAttachment(ctx, openID, messageID, i+1, urlValue)
		if err != nil {
			a.logWarn(ctx, "download qqofficial attachment failed", "url", urlValue, "error", err)
			continue
		}
		out = append(out, saved)
	}
	return out
}

func (a *Adapter) downloadInboundAttachment(ctx context.Context, openID, messageID string, index int, urlValue string) (savedAttachment, error) {
	artifactDir := strings.TrimSpace(a.cfg.ArtifactDir)
	if artifactDir == "" {
		return savedAttachment{}, fmt.Errorf("artifact dir is not configured")
	}
	absArtifactDir, err := filepath.Abs(artifactDir)
	if err != nil {
		return savedAttachment{}, fmt.Errorf("resolve artifact dir: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlValue, nil)
	if err != nil {
		return savedAttachment{}, err
	}
	resp, err := a.client.http.Do(req)
	if err != nil {
		return savedAttachment{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return savedAttachment{}, fmt.Errorf("download attachment http %d", resp.StatusCode)
	}
	name := inboundAttachmentName(urlValue, resp.Header, index)
	if err := os.MkdirAll(absArtifactDir, 0o755); err != nil {
		return savedAttachment{}, fmt.Errorf("create artifact dir: %w", err)
	}
	path := uniquePath(filepath.Join(absArtifactDir, name))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return savedAttachment{}, fmt.Errorf("create attachment file: %w", err)
	}
	defer file.Close()
	limited := io.LimitReader(resp.Body, maxInboundAttachmentBytes+1)
	written, err := io.Copy(file, limited)
	if err != nil {
		return savedAttachment{}, fmt.Errorf("write attachment file: %w", err)
	}
	if written > maxInboundAttachmentBytes {
		_ = file.Close()
		_ = os.Remove(path)
		return savedAttachment{}, fmt.Errorf("attachment exceeds %d bytes", maxInboundAttachmentBytes)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return savedAttachment{}, err
	}
	return savedAttachment{URL: urlValue, Path: absPath, Name: filepath.Base(absPath)}, nil
}

func inboundAttachmentName(urlValue string, header http.Header, index int) string {
	name := contentDispositionFilename(header.Get("Content-Disposition"))
	if name == "" {
		if parsed, err := url.Parse(urlValue); err == nil {
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
