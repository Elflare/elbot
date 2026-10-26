package agent

import (
	"context"
	"elbot/internal/agent/dialogue"
	agentexecution "elbot/internal/agent/execution"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/modelmgr"
	"elbot/internal/notification/rules"
	"elbot/internal/session"
	"elbot/internal/signal"
	"errors"
	"log/slog"
)

func (b *signalBindings) connectAgentNotifications(events signals) error {
	vision := rules.NewVisionNotices()
	return errors.Join(
		connectSignal(b, events.VisionFallbackUsed, func(ctx context.Context, e dialogue.VisionFallbackUsedEvent) error {
			return vision.Send(ctx, e.SessionID, e.Visible, e.Conversation)
		}, signal.ConnectOptions{}),
	)
}

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

// signalBindings owns subscriptions before platforms start, including
// partial startup failures. BeginClose wakes producers before waiting for exit.
type signalBindings struct {
	connections []*signal.Connection
}

func (b *signalBindings) connectSession(sessions *session.Service, rollback interface{ Forget(fileops.Binding) }, execution *agentexecution.ExecutionCoordinator) error {
	if err := connectSignal(b, sessions.ForegroundActivated(), func(ctx context.Context, event session.ForegroundActivatedEvent) error {
		execution.AdoptForeground(ctx, &event.Session, event.Binding)
		return nil
	}, signal.ConnectOptions{}); err != nil {
		return err
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

type signals struct {
	ModelCallCompleted *signal.Signal[agentshare.ModelCallCompletedEvent, signal.Empty]
	StatusChanged      *signal.Signal[agentexecution.StatusChangedEvent, signal.Empty]
	VisionFallbackUsed *signal.Signal[dialogue.VisionFallbackUsedEvent, signal.Empty]
}

func newSignals() signals {
	return signals{
		ModelCallCompleted: signal.New[agentshare.ModelCallCompletedEvent, signal.Empty]("agent.model_completed"),
		StatusChanged:      signal.New[agentexecution.StatusChangedEvent, signal.Empty]("agent.status_changed"),
		VisionFallbackUsed: signal.New[dialogue.VisionFallbackUsedEvent, signal.Empty]("agent.vision_fallback"),
	}
}
