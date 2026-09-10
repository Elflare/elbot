package qqonebot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/platform"
)

func (a *Adapter) ResolveMedia(ctx context.Context, segment platform.MessageSegment, maxBytes int64) (delivery.Source, error) {
	if segment.Size > maxBytes {
		return delivery.Source{}, fmt.Errorf("onebot media exceeds import limit of %d bytes", maxBytes)
	}
	if delivery.IsHTTPMediaSource(segment.URL) {
		return delivery.Source{URL: segment.URL, MIMEType: segment.MIMEType}, nil
	}
	file := firstNonEmpty(segment.PlatformFileID, segment.Name)
	if file == "" || a.transport == nil {
		return delivery.Source{}, fmt.Errorf("onebot media source unavailable")
	}
	var path, url string
	switch segment.Type {
	case platform.SegmentImage:
		data, err := a.transport.GetImage(ctx, file)
		if err != nil {
			return delivery.Source{}, err
		}
		path, url = data.File, data.URL
	case platform.SegmentFile:
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
