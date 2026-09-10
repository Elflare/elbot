package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/storage"
)

func (a *Agent) prepareMediaOutputs(ctx context.Context, outputs []delivery.Output) ([]delivery.Output, error) {
	if err := delivery.ValidateOutputs(outputs); err != nil {
		return nil, err
	}
	prepared := append([]delivery.Output(nil), outputs...)
	if a.media == nil {
		for _, out := range prepared {
			if out.Source.MediaID != "" {
				return nil, fmt.Errorf("media center is not configured")
			}
		}
		return prepared, nil
	}
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
			return nil, fmt.Errorf("prepare output media %d: %w", i, err)
		}
		prepared[i].Source = delivery.Source{MediaID: item.ID, MIMEType: item.MIMEType}
		if prepared[i].Name == "" {
			prepared[i].Name = item.Name
		}
	}
	return prepared, nil
}

// Resolve only the send copy; Hook events and queued outputs keep media IDs.
func (a *Agent) resolveMediaOutputs(ctx context.Context, outputs []delivery.Output) ([]delivery.Output, func(), error) {
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
			return nil, func() {}, err
		}
		cleanups = append(cleanups, release)
		resolved[i].Source = source
	}
	return resolved, cleanup, nil
}

func (a *Agent) sendPreparedMedia(ctx context.Context, outputs []delivery.Output, send func([]delivery.Output) (delivery.Receipt, error)) (delivery.Receipt, error) {
	prepared, err := a.prepareMediaOutputs(ctx, outputs)
	if err != nil {
		return delivery.Receipt{}, err
	}
	resolved, cleanup, err := a.resolveMediaOutputs(ctx, prepared)
	if err != nil {
		return delivery.Receipt{}, err
	}
	defer cleanup()
	receipt, sendErr := send(resolved)
	if err := a.cacheMediaReceipt(ctx, prepared, receipt); err != nil {
		if a.logger != nil {
			a.logger.ErrorContext(ctx, "cache sent media receipt failed", "error", err)
		}
	}
	return receipt, sendErr
}

func (a *Agent) cacheMediaReceipt(ctx context.Context, outputs []delivery.Output, receipt delivery.Receipt) error {
	if a.store == nil || a.store.Media() == nil || a.mediaRetentionDays <= 0 {
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
			if outputIndex < 0 || outputIndex >= len(outputs) {
				continue
			}
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
