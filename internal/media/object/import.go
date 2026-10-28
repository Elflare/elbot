package object

import (
	"bytes"
	"context"
	"crypto/sha256"
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

	"elbot/internal/media/share"
	"elbot/internal/storage"
)

// ImportBytes 复用统一导入链。
func (m *Manager) ImportBytes(ctx context.Context, data []byte, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
	return m.ImportReader(ctx, bytes.NewReader(data), int64(len(data)), input, limits)
}

// ImportURL 下载内容，来源中的凭据不会进入持久化记录或下载错误。
func (m *Manager) ImportURL(ctx context.Context, rawURL string, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return share.MediaInfo{}, fmt.Errorf("invalid media URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return share.MediaInfo{}, fmt.Errorf("create media request: %w", err)
	}
	if limits.DownloadTimeout <= 0 {
		limits.DownloadTimeout = m.options.DownloadTimeout
	}
	response, err := (&http.Client{Timeout: limits.DownloadTimeout}).Do(request)
	if err != nil {
		var downloadError *url.Error
		if errors.As(err, &downloadError) {
			err = downloadError.Err
		}
		return share.MediaInfo{}, fmt.Errorf("download media: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return share.MediaInfo{}, fmt.Errorf("download media: unexpected HTTP status %d", response.StatusCode)
	}
	if input.Source.URL == "" {
		input.Source.URL = u.String()
	}
	if input.MIMEType == "" {
		input.MIMEType = response.Header.Get("Content-Type")
	}
	if input.Name == "" {
		input.Name = u.String()
	}
	return m.ImportReader(ctx, response.Body, response.ContentLength, input, limits)
}

// ImportFile 打开普通文件并归档，调用方负责其业务路径范围。
func (m *Manager) ImportFile(ctx context.Context, path string, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return share.MediaInfo{}, fmt.Errorf("open media source: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return share.MediaInfo{}, fmt.Errorf("stat media source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return share.MediaInfo{}, fmt.Errorf("media source is not a regular file")
	}
	if input.Name == "" {
		input.Name = filepath.Base(path)
	}
	return m.ImportReader(ctx, file, info.Size(), input, limits)
}

// ImportReader 在限制内读取、识别类型、按需压缩、生成内容 ID 并归档。
// 输入由调用方关闭；同步读取在开始及进入对象提交阶段检查取消。
func (m *Manager) ImportReader(ctx context.Context, input io.Reader, size int64, spec share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
	if err := ctx.Err(); err != nil {
		return share.MediaInfo{}, err
	}
	maxBytes := m.options.MaxImportBytes
	if limits.MaxImportBytes > 0 && limits.MaxImportBytes < maxBytes {
		maxBytes = limits.MaxImportBytes
	}
	if size > maxBytes {
		return share.MediaInfo{}, fmt.Errorf("media exceeds import limit of %d bytes", maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(input, maxBytes+1))
	if err != nil {
		return share.MediaInfo{}, fmt.Errorf("read media: %w", err)
	}
	size = int64(len(data))
	if size > maxBytes {
		return share.MediaInfo{}, fmt.Errorf("media exceeds import limit of %d bytes", maxBytes)
	}
	spec = sanitizeInput(spec)
	detected := http.DetectContentType(data)
	if strings.HasPrefix(detected, "image/") {
		spec.MIMEType = detected
	}
	if spec.MIMEType == "" {
		spec.MIMEType = mime.TypeByExtension(filepath.Ext(spec.Name))
	}
	if spec.MIMEType == "" {
		spec.MIMEType = detected
	}
	spec.MIMEType, _, err = mime.ParseMediaType(spec.MIMEType)
	if err != nil {
		return share.MediaInfo{}, fmt.Errorf("invalid media MIME type: %w", err)
	}
	if strings.HasPrefix(spec.MIMEType, "image/") && shouldCompressImage(data, m.options.Media) {
		data, err = compressImage(data, m.options.Media.LLMImageCompressionThresholdBytes, m.options.Media.LLMImageMaxLength)
		if err != nil {
			return share.MediaInfo{}, fmt.Errorf("compress imported image: %w", err)
		}
		size = int64(len(data))
		if size > maxBytes {
			return share.MediaInfo{}, fmt.Errorf("compressed media exceeds import limit of %d bytes", maxBytes)
		}
		spec.Name = compressedName(spec.Name)
		spec.MIMEType = "image/jpeg"
	}
	sum := sha256.Sum256(data)
	id := fmt.Sprintf("%s%x", share.IDPrefix, sum)
	unlock, err := m.objects.acquire(ctx, id)
	if err != nil {
		return share.MediaInfo{}, err
	}
	defer unlock()
	if existing, err := m.media.Get(ctx, id); err == nil {
		if err := m.media.Touch(ctx, id, m.options.Now()); err != nil {
			return share.MediaInfo{}, err
		}
		return mediaInfo(existing), nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return share.MediaInfo{}, err
	}
	now := m.options.Now()
	item := &storage.Media{ID: id, Name: spec.Name, MIMEType: spec.MIMEType, Size: size, Backend: m.options.Backend,
		SourcePlatform: spec.Source.Platform, SourceURL: spec.Source.URL, SourceFileID: spec.Source.FileID, CreatedAt: now, LastAccessedAt: now}
	backend, err := m.backendForStoredMedia(item)
	if err != nil {
		return share.MediaInfo{}, err
	}
	location, err := backend.Put(ctx, id, bytes.NewReader(data), size, item.MIMEType)
	if err != nil {
		return share.MediaInfo{}, err
	}
	if item.Backend == "s3" {
		item.ObjectKey = location
	} else {
		item.LocalPath = location
	}
	if err := m.media.Upsert(ctx, item); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return share.MediaInfo{}, errors.Join(err, backend.Remove(cleanupCtx, location))
	}
	return mediaInfo(item), nil
}

// sanitizeInput 是展示名称与来源事实的唯一清理入口。
func sanitizeInput(input share.Input) share.Input {
	input.Name = share.SanitizeName(input.Name)
	input.MIMEType = strings.TrimSpace(input.MIMEType)
	input.Source.URL = sanitizeSourceURL(input.Source.URL)
	input.Source.FileID = sanitizeSourceFileID(input.Source.FileID)
	return input
}

// sanitizeSourceURL 仅保留不含凭据、查询及片段的 HTTP 地址。
func sanitizeSourceURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return ""
	}
	for _, component := range strings.Split(parsed.Path, "/") {
		if strings.HasPrefix(strings.ToLower(component), "bot") && strings.Contains(component, ":") {
			return ""
		}
	}
	return parsed.String()
}

// sanitizeSourceFileID 保留平台标识，不持久化 URL、内联内容或本地路径。
func sanitizeSourceFileID(id string) string {
	id = strings.TrimSpace(id)
	lower := strings.ToLower(id)
	if strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "base64:") || strings.ContainsAny(id, "/\\") {
		return ""
	}
	if parsed, err := url.Parse(id); err == nil {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "file":
			return ""
		}
	}
	if len(id) >= 2 && id[1] == ':' && ((id[0] >= 'a' && id[0] <= 'z') || (id[0] >= 'A' && id[0] <= 'Z')) {
		return ""
	}
	return id
}
