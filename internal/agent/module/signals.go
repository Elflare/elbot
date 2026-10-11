package module

import (
	"context"
	"errors"

	"elbot/internal/fileops"
	"elbot/internal/session"
	"elbot/internal/signal"
)

// signalBindings owns subscriptions before platforms start, including
// partial startup failures. BeginClose wakes producers before waiting for exit.
type signalBindings struct {
	connections []*signal.Connection
	displays    []*statusDisplay
}

func (b *signalBindings) connectSession(sessions *session.Service, rollback *fileops.RollbackManager) error {
	if rollback == nil {
		return nil
	}
	connection, err := sessions.BindingChanged().Observe(func(ctx context.Context, event session.BindingChangedEvent) error {
		if event.Old != nil {
			rollback.Forget(event.Old)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		return err
	}
	b.connections = append(b.connections, connection)
	return nil
}

func (b *signalBindings) BeginClose() {
	for _, connection := range b.connections {
		connection.BeginClose()
	}
	for _, display := range b.displays {
		display.BeginClose()
	}
}

func (b *signalBindings) Close(ctx context.Context) error {
	b.BeginClose()
	results := make(chan error, len(b.displays))
	for _, display := range b.displays {
		go func() { results <- display.Close(ctx) }()
	}
	var errs []error
	for _, connection := range b.connections {
		errs = append(errs, connection.Close(ctx))
	}
	for range len(b.displays) {
		errs = append(errs, <-results)
	}
	return errors.Join(errs...)
}

func (b *signalBindings) stopped() bool {
	for _, connection := range b.connections {
		select {
		case <-connection.Done():
		default:
			return false
		}
	}
	for _, display := range b.displays {
		if !display.stopped() {
			return false
		}
	}
	return true
}

func connectSignal[T any](b *signalBindings, source *signal.Signal[T, signal.Empty], handler func(context.Context, T) error, options signal.ConnectOptions) error {
	c, err := source.Observe(handler, options)
	if err == nil {
		b.connections = append(b.connections, c)
	}
	return err
}
