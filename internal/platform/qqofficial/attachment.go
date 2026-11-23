// attachment.go 将入站附件转换为媒体片段，整理类型、名称、来源和大小事实。
// 这里只解析附件元数据，不下载或持久化媒体。

package qqofficial

import (
	"elbot/internal/platform/share"
	"fmt"
	"mime"
	"net/url"
	"path/filepath"
	"strings"
)

func inboundAttachmentSegments(attachments []messageAttachment) []share.MessageSegment {
	segments := make([]share.MessageSegment, 0, len(attachments))
	for i, attachment := range attachments {
		kind := share.SegmentFile
		if isImageAttachment(attachment) {
			kind = share.SegmentImage
		}
		mimeType := attachmentDeclaredMIMEType(attachment)
		if mimeType == "file" {
			mimeType = ""
		}
		segments = append(segments, share.MessageSegment{Type: kind, URL: strings.TrimSpace(attachment.URL), Name: inboundAttachmentName(attachment, i+1), MIMEType: mimeType, Size: attachment.Size})
	}
	return segments
}

func inboundAttachmentName(attachment messageAttachment, index int) string {
	name := strings.TrimSpace(attachment.Filename)
	if name == "" {
		if parsed, err := url.Parse(attachment.URL); err == nil {
			name = filepath.Base(parsed.Path)
		}
	}
	name = sanitizeFilename(name)
	if name == "" || looksLikeQQDownloadName(name) {
		name = fmt.Sprintf("attachment-%d", index)
	}
	return name
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

func looksLikeQQDownloadName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(lower, "qqdownload") || strings.HasPrefix(lower, "robot1.0_") || len([]rune(lower)) > 96
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
