// media.go 按显式平台解析媒体，准备出站媒体并关联实际发送回执；解析不回退到当前或默认平台。
// 媒体服务与回执持久化函数均由根部注入，只借用，不管理其生命周期。

package dispatch

import (
	"context"
	"elbot/internal/operationresult"
	"fmt"
	"log/slog"
	"time"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/platform/share"
	"elbot/internal/storage"
)

// ResolveMedia 只使用指定平台，不沿用当前会话或默认发送平台。
func (r *Router) ResolveMedia(ctx context.Context, name string, input share.MessageSegment, maxBytes int64) (operationresult.Result[delivery.Source], error) {
	r.mu.RLock()
	sender := r.senders[name]
	r.mu.RUnlock()
	if sender == nil {
		return operationresult.Result[delivery.Source]{Status: operationresult.Unavailable, Message: fmt.Sprintf("target platform %q is not configured", name)}, nil
	}
	resolver, ok := sender.(share.MediaResolver)
	if !ok {
		return operationresult.Result[delivery.Source]{Status: operationresult.Incompatible, Message: fmt.Sprintf("platform %q does not support media resolution", name)}, nil
	}
	return resolver.ResolveMedia(ctx, input, maxBytes)
}

func mediaOutputRequests(outputs []delivery.Output) []media.OutputRequest {
	var requests []media.OutputRequest
	for i, out := range outputs {
		if isMediaOutputKind(out.Kind) {
			requests = append(requests, media.OutputRequest{Index: i, Name: out.Name, Source: out.Source})
		}
	}
	return requests
}

func (a *Router) cacheMediaReceipt(ctx context.Context, outputs []delivery.Output, mediaIDs map[int]string, receipt delivery.Receipt) {
	if a.mediaRetentionDays <= 0 || len(mediaIDs) == 0 {
		return
	}
	expiresAt := time.Now().AddDate(0, 0, a.mediaRetentionDays)
	for _, message := range receipt.SentMessages {
		segmentIndex := 0
		for _, outputIndex := range message.OutputIndexes {
			id, ok := mediaIDs[outputIndex]
			if !ok {
				continue
			}
			if err := a.saveMediaOutput(ctx, storage.MediaOutput{Platform: message.Platform, ChatType: message.ChatType, ChatID: message.ChatID, MessageID: message.PlatformMessageID, SegmentIndex: segmentIndex, Kind: string(outputs[outputIndex].Kind), MediaID: id, ExpiresAt: expiresAt}); err != nil {
				globalevents.AsyncLog(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelWarn, EventName: "cache_sent_media_receipt_failed", Module: "delivery", Content: err.Error(), Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.LogStatus(err)})
			}
			segmentIndex++
		}
	}
}

func isMediaOutputKind(kind delivery.Kind) bool {
	return kind == delivery.KindImage || kind == delivery.KindFile || kind == delivery.KindRecord
}
