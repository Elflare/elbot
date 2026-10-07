package app

import (
	"context"
	"errors"

	"elbot/internal/fileops"
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

func (b *signalBindings) connectSession(sessions *session.Service, rollback *fileops.RollbackManager) error {
	if rollback == nil {
		return nil
	}
	queue, err := signal.NewQueue(signal.QueueOptions{Name: "session.rollback_cleanup"})
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

func (b *signalBindings) newQueue(name string, backpressure bool) (*signal.Queue, error) {
	queue, err := signal.NewQueue(signal.QueueOptions{Name: name, WaitForCapacity: backpressure})
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
