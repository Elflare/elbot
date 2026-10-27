// media.go 准备发送媒体并处理投递后的媒体缓存与回执关联。

package dispatch

import (
	"context"
	"elbot/internal/contextinfo"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

func (a *Router) prepareMediaOutputs(ctx context.Context, outputs []delivery.Output) ([]delivery.Output, globalevents.LogResult, error) {
	// The owning delivery.Manager validates outputs before entering rawSender.
	prepared := append([]delivery.Output(nil), outputs...)
	for i, out := range prepared {
		if out.Kind != delivery.KindImage && out.Kind != delivery.KindFile && out.Kind != delivery.KindRecord {
			continue
		}
		input := media.Input{Name: out.Name, MIMEType: out.Source.MIMEType}
		var item *storage.Media
		var err error
		switch {
		case out.Source.MediaID != "":
			item, err = a.media.Metadata(ctx, out.Source.MediaID)
		case out.Source.URL != "":
			input.Source.URL = out.Source.URL
			item, err = a.media.ImportURL(ctx, out.Source.URL, input)
		case out.Source.Path != "":
			item, err = a.media.ImportFile(ctx, out.Source.Path, input)
		case len(out.Source.Data) > 0:
			item, err = a.media.ImportBytes(ctx, out.Source.Data, input)
		}
		if err != nil {
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("prepare output media %d: %v", i, err)}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return nil, logResult, fmt.Errorf("prepare output media %d: %w", i, err)
		}
		prepared[i].Source = delivery.Source{MediaID: item.ID, MIMEType: item.MIMEType}
		if prepared[i].Name == "" {
			prepared[i].Name = item.Name
		} else {
			prepared[i].Name = media.SanitizeName(prepared[i].Name)
		}
	}
	return prepared, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

// Resolve only the send copy; Hook events and queued outputs keep media IDs.
func (a *Router) resolveMediaOutputs(ctx context.Context, outputs []delivery.Output) ([]delivery.Output, func(), globalevents.LogResult, error) {
	resolved := append([]delivery.Output(nil), outputs...)
	var cleanups []func()
	cleanup := func() {
		for _, fn := range cleanups {
			fn()
		}
	}
	for i, out := range resolved {
		if out.Source.MediaID == "" {
			continue
		}
		source, release, err := a.media.ResolveForOutput(ctx, out.Source.MediaID)
		if err != nil {
			cleanup()
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("resolve output media %d: %v", i, err)}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return nil, func() {}, logResult, err
		}
		cleanups = append(cleanups, release)
		resolved[i].Source = source
	}
	return resolved, cleanup, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Router) sendPreparedMedia(ctx context.Context, outputs []delivery.Output, send func([]delivery.Output) (delivery.Receipt, globalevents.LogResult, error)) (delivery.Receipt, globalevents.LogResult, error) {
	prepared, logResult, err := a.prepareMediaOutputs(ctx, outputs)
	if err != nil {
		return delivery.Receipt{}, logResult, err
	}
	resolved, cleanup, logResult, err := a.resolveMediaOutputs(ctx, prepared)
	if err != nil {
		return delivery.Receipt{}, logResult, err
	}
	defer cleanup()
	receipt, logResult, sendErr := send(resolved)
	if logResult.Content != "" {
		logResult.Content += "\n"
	}
	logResult.Content += fmt.Sprintf("platform_message_ids=%v", receipt.PlatformMessageIDs)
	for _, sent := range receipt.SentMessages {
		logResult.Content += fmt.Sprintf("\nsent_message: platform=%s scope_id=%s platform_message_id=%s output_indexes=%v", sent.Platform, sent.ScopeID, sent.PlatformMessageID, sent.OutputIndexes)
	}
	if err := a.cacheMediaReceipt(ctx, prepared, receipt); err != nil {

		{
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
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
		}

	}
	return receipt, logResult, sendErr
}

func (a *Router) cacheMediaReceipt(ctx context.Context, outputs []delivery.Output, receipt delivery.Receipt) error {
	if a.mediaRetentionDays <= 0 {
		return nil
	}
	sent := receipt.SentMessages
	expiresAt := time.Now().AddDate(0, 0, a.mediaRetentionDays)
	var errs []error
	for _, message := range sent {
		platformName := strings.TrimSpace(message.Platform)
		scopeID := strings.TrimSpace(message.ScopeID)
		messageID := strings.TrimSpace(message.PlatformMessageID)
		if platformName == "" || scopeID == "" || messageID == "" {
			continue
		}
		segmentIndex := 0
		for _, outputIndex := range message.OutputIndexes {
			out := outputs[outputIndex]
			if out.Source.MediaID == "" || !isMediaOutputKind(out.Kind) {
				continue
			}
			if err := a.store.Media().SaveOutput(ctx, storage.MediaOutput{Platform: platformName, ScopeID: scopeID, MessageID: messageID, SegmentIndex: segmentIndex, Kind: string(out.Kind), MediaID: out.Source.MediaID, ExpiresAt: expiresAt}); err != nil {
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
