package app

import (
	"context"
	"errors"
	"log/slog"

	"elbot/internal/platform"
	"elbot/internal/signal"
)

// signalBindings is assembled before platforms run and closed after they stop.
// It owns subscriptions and queues even if subsequent startup stages fail.
type signalBindings struct {
	connections []*signal.Connection
	queues      []*signal.Queue
}

func (b *signalBindings) connectPlatforms(agt platformHookAgent, adapters []platformRuntime, logger *slog.Logger) error {
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		agt.RegisterPlatformSender(adapter.Name(), adapter)
		source, ok := adapter.(platform.ConnectionSource)
		if !ok {
			continue
		}
		queue, err := signal.NewQueue(signal.QueueOptions{Name: adapter.Name() + ".connected", Logger: logger})
		if err != nil {
			return err
		}
		b.queues = append(b.queues, queue)
		name := adapter.Name()
		connection, err := source.ConnectedSignal().Connect(func(ctx context.Context, event platform.ConnectedEvent) error {
			platformName := event.Platform
			if platformName == "" {
				platformName = name
			}
			agt.NotifyPlatformConnected(ctx, platformName)
			return nil
		}, signal.ConnectOptions{Executor: queue, Lifetime: signal.FollowExecutor, Shutdown: signal.CancelPending})
		if err != nil {
			return err
		}
		b.connections = append(b.connections, connection)
	}
	return nil
}

func (b *signalBindings) Close(ctx context.Context) error {
	for _, connection := range b.connections {
		connection.Disconnect()
	}
	results := make(chan error, len(b.queues))
	for _, queue := range b.queues {
		go func() { results <- queue.Close(ctx) }()
	}
	var errs []error
	for range b.queues {
		errs = append(errs, <-results)
	}
	return errors.Join(errs...)
}

func (b *signalBindings) stopped() bool {
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
