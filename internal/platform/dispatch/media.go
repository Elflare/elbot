// media.go 按显式平台解析媒体，准备出站媒体并关联实际发送回执；解析不回退到当前或默认平台。
// 媒体服务与回执持久化函数均由根部注入，只借用，不管理其生命周期。

package dispatch

import (
	"context"
	"elbot/internal/contextinfo"
	"errors"
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
func (r *Router) ResolveMedia(ctx context.Context, name string, input share.MessageSegment, maxBytes int64) (delivery.Source, error) {
	r.mu.RLock()
	sender := r.senders[name]
	r.mu.RUnlock()
	if sender == nil {
		return delivery.Source{}, fmt.Errorf("target platform %q is not configured", name)
	}
	resolver, ok := sender.(share.MediaResolver)
	if !ok {
		return delivery.Source{}, fmt.Errorf("platform %q does not support media resolution", name)
	}
	return resolver.ResolveMedia(ctx, input, maxBytes)
}

func (a *Router) prepareMediaOutputs(ctx context.Context, outputs []delivery.Output) ([]delivery.Output, error) {
	// The owning delivery.Manager validates outputs before entering rawSender.
	prepared := append([]delivery.Output(nil), outputs...)
	for i, out := range prepared {
		if !isMediaOutputKind(out.Kind) {
			continue
		}
		input := media.Input{Name: out.Name, MIMEType: out.Source.MIMEType}
		var item media.MediaInfo
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
			return nil, fmt.Errorf("prepare output media %d: %w", i, err)
		}
		prepared[i].Source = delivery.Source{MediaID: item.ID, MIMEType: item.MIMEType}
		if prepared[i].Name == "" {
			prepared[i].Name = item.Name
		} else {
			prepared[i].Name = media.SanitizeName(prepared[i].Name)
		}
	}
	return prepared, nil
}

// Resolve only the send copy; Hook events and queued outputs keep media IDs.
func (a *Router) resolveMediaOutputs(ctx context.Context, outputs []delivery.Output) ([]delivery.Output, []media.UsageID, error) {
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
			return nil, usages, err
		}
		resolved[i].Source = source
	}
	return resolved, usages, nil
}

func (a *Router) sendPreparedMedia(ctx context.Context, outputs []delivery.Output, send func([]delivery.Output) (delivery.Receipt, error)) (receipt delivery.Receipt, err error) {
	prepared, err := a.prepareMediaOutputs(ctx, outputs)
	if err != nil {
		return delivery.Receipt{}, err
	}
	resolved, usages, err := a.resolveMediaOutputs(ctx, prepared)
	if len(usages) != 0 {
		defer a.publishUsageFinished(ctx, usages)
	}
	if err != nil {
		return delivery.Receipt{}, err
	}
	receipt, sendErr := send(resolved)
	if err := a.cacheMediaReceipt(ctx, prepared, receipt); err != nil {
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelWarn,
			EventName:    "cache_sent_media_receipt_failed",
			Module:       "delivery",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("cache sent media receipt failed\nerror=%v", err),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultFailed,
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		record.Identity = globalevents.LogIdentityFromExecution(facts)
		globalevents.Log(context.WithoutCancel(ctx), record)
	}
	return receipt, sendErr
}

func (a *Router) cacheMediaReceipt(ctx context.Context, outputs []delivery.Output, receipt delivery.Receipt) error {
	if a.mediaRetentionDays <= 0 {
		return nil
	}
	expiresAt := time.Now().AddDate(0, 0, a.mediaRetentionDays)
	var errs []error
	for _, message := range receipt.SentMessages {
		segmentIndex := 0
		for _, outputIndex := range message.OutputIndexes {
			out := outputs[outputIndex]
			if out.Source.MediaID == "" || !isMediaOutputKind(out.Kind) {
				continue
			}
			if err := a.saveMediaOutput(ctx, storage.MediaOutput{Platform: message.Platform, ChatType: message.ChatType, ChatID: message.ChatID, MessageID: message.PlatformMessageID, SegmentIndex: segmentIndex, Kind: string(out.Kind), MediaID: out.Source.MediaID, ExpiresAt: expiresAt}); err != nil {
				errs = append(errs, err)
			}
			segmentIndex++
		}
	}
	return errors.Join(errs...)
}

func isMediaOutputKind(kind delivery.Kind) bool {
	return kind == delivery.KindImage || kind == delivery.KindFile || kind == delivery.KindRecord
}
