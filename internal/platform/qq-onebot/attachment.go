// attachment.go 负责图片、文件的媒体来源解析与大小限制检查。
// 返回媒体服务可使用的 URL 或路径，平台文件信息按需查询。

package qqonebot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/platform/share"
)

func (a *Adapter) ResolveMedia(ctx context.Context, segment share.MessageSegment, maxBytes int64) (delivery.Source, error) {
	if segment.Size > maxBytes {
		return delivery.Source{}, fmt.Errorf("onebot media exceeds import limit of %d bytes", maxBytes)
	}
	if delivery.IsHTTPMediaSource(segment.URL) {
		return delivery.Source{URL: segment.URL, MIMEType: segment.MIMEType}, nil
	}
	file := firstNonEmpty(segment.PlatformFileID, segment.Name)
	if file == "" {
		return delivery.Source{}, fmt.Errorf("onebot media source unavailable")
	}
	var path, url string
	switch segment.Type {
	case share.SegmentImage:
		data, err := a.transport.GetImage(ctx, file)
		if err != nil {
			return delivery.Source{}, err
		}
		path, url = data.File, data.URL
	case share.SegmentFile:
		data, err := a.transport.GetFile(ctx, file)
		if err != nil {
			return delivery.Source{}, err
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(data.FileSize), 10, 64)
		if size > maxBytes {
			return delivery.Source{}, fmt.Errorf("onebot media exceeds import limit of %d bytes", maxBytes)
		}
		path, url = data.File, data.URL
	default:
		return delivery.Source{}, fmt.Errorf("unsupported onebot media kind %q", segment.Type)
	}
	if delivery.IsHTTPMediaSource(url) {
		return delivery.Source{URL: strings.TrimSpace(url), MIMEType: segment.MIMEType}, nil
	}
	if strings.TrimSpace(path) == "" {
		return delivery.Source{}, fmt.Errorf("onebot media source unavailable")
	}
	return delivery.Source{Path: strings.TrimSpace(path), MIMEType: segment.MIMEType}, nil
}
