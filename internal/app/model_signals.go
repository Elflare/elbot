package app

import (
	"context"

	"elbot/internal/modelmgr"
	"elbot/internal/notification"
	"elbot/internal/notification/rules"
	"elbot/internal/signal"
)

func (b *signalBindings) connectModels(models *modelmgr.Service, notices *notification.Manager) error {
	queue, err := b.newQueue("model.notifications", false)
	if err != nil {
		return err
	}
	return connectSignal(b, models.ModelRetrying(), func(ctx context.Context, event modelmgr.ModelRetryingEvent) error {
		return rules.ModelRetry(notices)(ctx, event.Provider, event.Retry)
	}, signal.ConnectOptions{Executor: queue, Lifetime: signal.FollowEmit, Shutdown: signal.CancelPending})
}
