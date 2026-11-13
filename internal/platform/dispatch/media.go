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

func (a *Router) prepareMediaOutputs(ctx context.Context, outputs []delivery.Output) (operationresult.Result[[]delivery.Output], error) {
	// SendTo validates source outputs before media preparation.
	prepared := append([]delivery.Output(nil), outputs...)
	for i, out := range prepared {
		if !isMediaOutputKind(out.Kind) {
			continue
		}
		input := media.Input{Name: out.Name, MIMEType: out.Source.MIMEType}
		var item operationresult.Result[media.MediaInfo]
		var err error
		switch {
		case out.Source.MediaID != "":
			item, err = a.media.Info(ctx, out.Source.MediaID)
		case out.Source.URL != "":
			input.Source.URL = out.Source.URL
			item, err = a.media.ImportURL(ctx, out.Source.URL, input, media.ImportLimits{})
		case out.Source.Path != "":
			item, err = a.media.ImportFile(ctx, out.Source.Path, input, media.ImportLimits{})
		case len(out.Source.Data) > 0:
			item, err = a.media.ImportBytes(ctx, out.Source.Data, input, media.ImportLimits{})
		}
		if err != nil {
			return operationresult.Result[[]delivery.Output]{}, fmt.Errorf("prepare output media %d: %w", i, err)
		}
		if item.Status != operationresult.Success {
			return operationresult.Result[[]delivery.Output]{Status: item.Status, Message: item.Message}, nil
		}
		prepared[i].Source = delivery.Source{MediaID: item.Data.ID, MIMEType: item.Data.MIMEType}
		if prepared[i].Name == "" {
			prepared[i].Name = item.Data.Name
		} else {
			prepared[i].Name = media.SanitizeName(prepared[i].Name)
		}
	}
	return operationresult.Result[[]delivery.Output]{Status: operationresult.Success, Data: prepared}, nil
}

// Resolve only the send copy; Hook events and queued outputs keep media IDs.
func (a *Router) resolveMediaOutputs(ctx context.Context, outputs []delivery.Output) (operationresult.Result[[]delivery.Output], []media.UsageID, error) {
	resolved := append([]delivery.Output(nil), outputs...)
	var usages []media.UsageID
	for i, out := range resolved {
		if out.Source.MediaID == "" {
			continue
		}
		source, usageID, err := a.media.ResolveForOutput(ctx, out.Source.MediaID)
		if usageID != "" {
			usages = append(usages, usageID)
		}
		if err != nil {
			return operationresult.Result[[]delivery.Output]{}, usages, err
		}
		if source.Status != operationresult.Success {
			return operationresult.Result[[]delivery.Output]{Status: source.Status, Message: source.Message}, usages, nil
		}
		resolved[i].Source = source.Data
	}
	return operationresult.Result[[]delivery.Output]{Status: operationresult.Success, Data: resolved}, usages, nil
}

func (a *Router) sendPreparedMedia(ctx context.Context, outputs []delivery.Output, send func([]delivery.Output) (delivery.Receipt, error)) (operationresult.Result[delivery.Receipt], error) {
	prepared, err := a.prepareMediaOutputs(ctx, outputs)
	if err != nil {
		return operationresult.Result[delivery.Receipt]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[delivery.Receipt]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	resolved, usages, err := a.resolveMediaOutputs(ctx, prepared.Data)
	if len(usages) != 0 {
		defer a.publishUsageFinished(ctx, usages)
	}
	if err != nil {
		return operationresult.Result[delivery.Receipt]{}, err
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[delivery.Receipt]{Status: resolved.Status, Message: resolved.Message}, nil
	}
	receipt, sendErr := send(resolved.Data)
	if sendErr != nil {
		return operationresult.Result[delivery.Receipt]{}, sendErr
	}
	a.cacheMediaReceipt(ctx, prepared.Data, receipt)
	return operationresult.Result[delivery.Receipt]{Status: operationresult.Success, Data: receipt}, nil
}

func (a *Router) cacheMediaReceipt(ctx context.Context, outputs []delivery.Output, receipt delivery.Receipt) {
	if a.mediaRetentionDays <= 0 {
		return
	}
	expiresAt := time.Now().AddDate(0, 0, a.mediaRetentionDays)
	for _, message := range receipt.SentMessages {
		segmentIndex := 0
		for _, outputIndex := range message.OutputIndexes {
			out := outputs[outputIndex]
			if out.Source.MediaID == "" || !isMediaOutputKind(out.Kind) {
				continue
			}
			if err := a.saveMediaOutput(ctx, storage.MediaOutput{Platform: message.Platform, ChatType: message.ChatType, ChatID: message.ChatID, MessageID: message.PlatformMessageID, SegmentIndex: segmentIndex, Kind: string(out.Kind), MediaID: out.Source.MediaID, ExpiresAt: expiresAt}); err != nil {
				globalevents.AsyncLog(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelWarn, EventName: "cache_sent_media_receipt_failed", Module: "delivery", Content: err.Error(), Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.LogStatus(err)})
			}
			segmentIndex++
		}
	}
}

func isMediaOutputKind(kind delivery.Kind) bool {
	return kind == delivery.KindImage || kind == delivery.KindFile || kind == delivery.KindRecord
}
