package module

import (
	"context"
	"errors"

	"elbot/internal/agent"
	"elbot/internal/fileops"
	"elbot/internal/session"
	"elbot/internal/signal"
)

// signalBindings owns subscriptions before platforms start, including
// partial startup failures. BeginClose wakes producers before waiting for exit.
type signalBindings struct {
	connections []*signal.Connection
}

func (b *signalBindings) connectSession(sessions *session.Service, rollback *fileops.RollbackManager, execution *agent.ExecutionCoordinator) error {
	if err := connectSignal(b, sessions.ForegroundActivated(), func(ctx context.Context, event session.ForegroundActivatedEvent) error {
		execution.AdoptForeground(ctx, &event.Session, event.Binding)
		return nil
	}, signal.ConnectOptions{}); err != nil {
		return err
	}
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
}

func (b *signalBindings) Close(ctx context.Context) error {
	b.BeginClose()
	var errs []error
	for _, connection := range b.connections {
		errs = append(errs, connection.Close(ctx))
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
	return true
}

func connectSignal[T any](b *signalBindings, source *signal.Signal[T, signal.Empty], handler func(context.Context, T) error, options signal.ConnectOptions) error {
	c, err := source.Observe(handler, options)
	if err == nil {
		b.connections = append(b.connections, c)
	}
	return err
}
