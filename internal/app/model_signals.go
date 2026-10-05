package app

import (
	"context"
	"log/slog"

	"elbot/internal/modelmgr"
	"elbot/internal/notification"
	"elbot/internal/notification/rules"
	"elbot/internal/signal"
)

func (b *signalBindings) newQueue(name string, logger *slog.Logger, backpressure bool) (*signal.Queue, error) {
	queue, err := signal.NewQueue(signal.QueueOptions{Name: name, Logger: logger, WaitForCapacity: backpressure})
	if err == nil {
		b.queues = append(b.queues, queue)
	}
	return queue, err
}

func connectSignal[T any](b *signalBindings, source *signal.Signal[T], handler signal.Handler[T], options signal.ConnectOptions) error {
	c, err := source.Connect(handler, options)
	if err == nil {
		b.connections = append(b.connections, c)
	}
	return err
}

func (b *signalBindings) connectModels(models *modelmgr.Service, notices *notification.Manager, logger *slog.Logger) error {
	queue, err := b.newQueue("model.notifications", logger, false)
	if err != nil {
		return err
	}
	return connectSignal(b, models.ModelRetrying(), func(ctx context.Context, event modelmgr.ModelRetryingEvent) error {
		return rules.ModelRetry(notices)(ctx, event.Provider, event.Retry)
	}, signal.ConnectOptions{Executor: queue, Lifetime: signal.FollowEmit, Shutdown: signal.CancelPending})
}
