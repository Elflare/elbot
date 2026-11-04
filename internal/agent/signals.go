package agent

import (
	"context"
	agentexecution "elbot/internal/agent/execution"
	"elbot/internal/media"
	"elbot/internal/session"
	"elbot/internal/signal"
	"errors"
)

func publishMediaUsageFinished(center *media.MediaCenter) func(context.Context, media.UsageID) {
	return func(ctx context.Context, id media.UsageID) {
		center.PublishUsageFinished(ctx, []media.UsageID{id})
	}
}

// signalBindings owns subscriptions before platforms start, including
// partial startup failures. BeginClose wakes producers before waiting for exit.
type signalBindings struct {
	connections []*signal.Connection
}

func (b *signalBindings) connectSession(sessions *session.Supervisor, execution *agentexecution.ExecutionCoordinator) error {
	connection, err := sessions.ObserveForegroundActivated(func(ctx context.Context, event session.ForegroundActivatedEvent) error {
		execution.AdoptForeground(ctx, &event.Session, event.Binding)
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

type signals struct {
	StatusChanged *signal.Async[agentexecution.StatusChangedEvent]
}

func newSignals() signals {
	return signals{
		StatusChanged: signal.NewAsync[agentexecution.StatusChangedEvent]("agent.status_changed"),
	}
}
