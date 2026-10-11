package module

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/modelmgr"
	"elbot/internal/notification/rules"
	"elbot/internal/signal"
	"log/slog"
)

func (b *signalBindings) connectModels(models *modelmgr.Service) error {
	return connectSignal(b, models.ModelRetrying(), func(ctx context.Context, e modelmgr.ModelRetryingEvent) error {
		ctx = e.CallContext
		if ctx.Err() != nil {
			return nil
		}
		text := rules.ModelRetry(e.Provider, e.Retry)
		if text == "" {
			return nil
		}
		_, err := events.NotificationRequested.EmitAsync(ctx, events.NewNotification(ctx, slog.LevelWarn, text))
		return err
	}, signal.ConnectOptions{})
}
