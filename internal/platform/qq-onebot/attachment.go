// attachment.go 负责图片、文件的媒体来源解析与大小限制检查。
// 返回媒体服务可使用的 URL 或路径，平台文件信息按需查询。

package qqonebot

import (
	"context"
	"elbot/internal/operationresult"
	"fmt"
	"strconv"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/platform/share"
)

func (a *Adapter) ResolveMedia(ctx context.Context, segment share.MessageSegment, maxBytes int64) (operationresult.Result[delivery.Source], error) {
	if segment.Size > maxBytes {
		return operationresult.Result[delivery.Source]{Status: operationresult.Rejected, Message: fmt.Sprintf("onebot media exceeds import limit of %d bytes", maxBytes)}, nil
	}
	if delivery.IsHTTPMediaSource(segment.URL) {
		return operationresult.Result[delivery.Source]{Status: operationresult.Success, Data: delivery.Source{URL: segment.URL, MIMEType: segment.MIMEType}}, nil
	}
	file := firstNonEmpty(segment.PlatformFileID, segment.Name)
	if file == "" {
		return operationresult.Result[delivery.Source]{Status: operationresult.Unavailable, Message: "onebot media source unavailable"}, nil
	}
	var path, url string
	switch segment.Type {
	case share.SegmentImage:
		data, err := a.transport.GetImage(ctx, file)
		if err != nil {
			return operationresult.Result[delivery.Source]{}, err
		}
		path, url = data.File, data.URL
	case share.SegmentFile:
		data, err := a.transport.GetFile(ctx, file)
		if err != nil {
			return operationresult.Result[delivery.Source]{}, err
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(data.FileSize), 10, 64)
		if size > maxBytes {
			return operationresult.Result[delivery.Source]{Status: operationresult.Rejected, Message: fmt.Sprintf("onebot media exceeds import limit of %d bytes", maxBytes)}, nil
		}
		path, url = data.File, data.URL
	default:
		return operationresult.Result[delivery.Source]{Status: operationresult.InvalidTarget, Message: fmt.Sprintf("unsupported onebot media kind %q", segment.Type)}, nil
	}
	if delivery.IsHTTPMediaSource(url) {
		return operationresult.Result[delivery.Source]{Status: operationresult.Success, Data: delivery.Source{URL: strings.TrimSpace(url), MIMEType: segment.MIMEType}}, nil
	}
	if strings.TrimSpace(path) == "" {
		return operationresult.Result[delivery.Source]{Status: operationresult.Unavailable, Message: "onebot media source unavailable"}, nil
	}
	return operationresult.Result[delivery.Source]{Status: operationresult.Success, Data: delivery.Source{Path: strings.TrimSpace(path), MIMEType: segment.MIMEType}}, nil
}
