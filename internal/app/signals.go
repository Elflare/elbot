package app

import (
	"context"
	"errors"
	"log/slog"

	elcron "elbot/internal/cron"
	"elbot/internal/fileops"
	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/signal"
)

// signalBindings owns subscriptions and queues before platforms start, including
// partial startup failures. BeginClose wakes producers before waiting for exit.
type signalBindings struct {
	connections []*signal.Connection
	queues      []*signal.Queue
	displays    []*statusDisplay
}

func (b *signalBindings) connectSession(sessions *session.Service, rollback *fileops.RollbackManager, logger *slog.Logger) error {
	if rollback == nil {
		return nil
	}
	queue, err := signal.NewQueue(signal.QueueOptions{Name: "session.rollback_cleanup", Logger: logger})
	if err != nil {
		return err
	}
	b.queues = append(b.queues, queue)
	connection, err := sessions.BindingChanged().Connect(func(ctx context.Context, event session.BindingChangedEvent) error {
		if event.Old != nil {
			rollback.Forget(event.Old)
		}
		return nil
	}, signal.ConnectOptions{Executor: queue, Lifetime: signal.FollowExecutor, Shutdown: signal.CancelPending})
	if err != nil {
		return err
	}
	b.connections = append(b.connections, connection)
	return nil
}

func (b *signalBindings) connectPlatforms(agt platformHookAgent, cron *elcron.Service, adapters []platformRuntime, logger *slog.Logger) error {
	consumers := []struct {
		name   string
		notify func(context.Context, string)
	}{{"hooks", agt.NotifyPlatformConnected}}
	if cron != nil {
		consumers = append(consumers, struct {
			name   string
			notify func(context.Context, string)
		}{"cron", cron.NotifyPlatformConnected})
	}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		source, ok := adapter.(platform.ConnectionSource)
		if !ok {
			continue
		}
		name := adapter.Name()
		for _, consumer := range consumers {
			queue, err := signal.NewQueue(signal.QueueOptions{Name: name + ".connected." + consumer.name, Logger: logger})
			if err != nil {
				return err
			}
			b.queues = append(b.queues, queue)
			connection, err := source.ConnectedSignal().Connect(func(ctx context.Context, event platform.ConnectedEvent) error {
				platformName := event.Platform
				if platformName == "" {
					platformName = name
				}
				consumer.notify(ctx, platformName)
				return nil
			}, signal.ConnectOptions{Executor: queue, Lifetime: signal.FollowExecutor, Shutdown: signal.CancelPending})
			if err != nil {
				return err
			}
			b.connections = append(b.connections, connection)
		}
	}
	return nil
}

func (b *signalBindings) BeginClose() {
	for _, connection := range b.connections {
		connection.Disconnect()
	}
	for _, queue := range b.queues {
		queue.BeginClose()
	}
	for _, display := range b.displays {
		display.BeginClose()
	}
}

func (b *signalBindings) Close(ctx context.Context) error {
	b.BeginClose()
	results := make(chan error, len(b.queues)+len(b.displays))
	for _, display := range b.displays {
		go func() { results <- display.Close(ctx) }()
	}
	for _, queue := range b.queues {
		go func() { results <- queue.Close(ctx) }()
	}
	var errs []error
	for range len(b.queues) + len(b.displays) {
		errs = append(errs, <-results)
	}
	return errors.Join(errs...)
}

func (b *signalBindings) stopped() bool {
	for _, display := range b.displays {
		if !display.stopped() {
			return false
		}
	}
	for _, queue := range b.queues {
		select {
		case <-queue.Done():
		default:
			return false
		}
	}
	return true
}

// withoutShutdownError removes expected cancellation leaves, not entire joined
// errors. In particular, a failed startup must survive a simultaneous shutdown.
func withoutShutdownError(err, expected error) error {
	if err == nil || expected == nil {
		return err
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var kept []error
		for _, child := range joined.Unwrap() {
			kept = append(kept, withoutShutdownError(child, expected))
		}
		return errors.Join(kept...)
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok && wrapped.Unwrap() != nil {
		// Keep the original wrapper when it contains no expected cancellation.
		if !errors.Is(err, expected) {
			return err
		}
		return withoutShutdownError(wrapped.Unwrap(), expected)
	}
	if errors.Is(err, expected) {
		return nil
	}
	return err
}
