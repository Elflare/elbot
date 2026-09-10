package qqofficial

import (
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"elbot/internal/platform"
)

func inboundAttachmentSegments(attachments []messageAttachment) []platform.MessageSegment {
	segments := make([]platform.MessageSegment, 0, len(attachments))
	for i, attachment := range attachments {
		kind := platform.SegmentFile
		if isImageAttachment(attachment) {
			kind = platform.SegmentImage
		}
		mimeType := attachmentDeclaredMIMEType(attachment)
		if mimeType == "file" {
			mimeType = ""
		}
		segments = append(segments, platform.MessageSegment{Type: kind, URL: strings.TrimSpace(attachment.URL), Name: inboundAttachmentName(attachment, nil, i+1), MIMEType: mimeType, Size: attachment.Size})
	}
	return segments
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

func attachmentDeclaredMIMEType(attachment messageAttachment) string {
	mimeType := strings.TrimSpace(attachment.ContentType)
	if mediaType, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = mediaType
	}
	return strings.ToLower(mimeType)
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
