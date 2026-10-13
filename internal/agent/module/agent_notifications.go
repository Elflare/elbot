package module

import (
	"context"
	agentevents "elbot/internal/agent/events"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/notification/rules"
	"elbot/internal/session"
	"elbot/internal/signal"
	"errors"
	"log/slog"
)

func (b *signalBindings) connectAgentNotifications(events agentevents.Signals) error {
	vision := rules.NewVisionNotices()
	return errors.Join(
		connectSignal(b, events.VisionFallbackUsed, func(ctx context.Context, e agentevents.VisionFallbackUsedEvent) error {
			return vision.Send(ctx, e.SessionID, e.Visible)
		}, signal.ConnectOptions{}),
		connectSignal(b, events.HookFailed, func(ctx context.Context, e agentevents.HookFailedEvent) error {
			if !e.Notice {
				return nil
			}
			text := rules.HookFailure(hook.Event{Point: e.Point, Platform: e.Platform}, e.Err)
			if text == "" {
				return nil
			}
			n := globalevents.NewNotification(ctx, slog.LevelError, text)
			if n.Origin == nil {
				n.Target.Platform = e.Platform.Name
				n.Target.ScopeID = e.Platform.ScopeID
				n.Target.ID = e.Platform.ConversationID
			}
			_, err := globalevents.NotificationRequested.EmitAsync(session.WithBinding(context.WithoutCancel(ctx), nil), n)
			return err
		}, signal.ConnectOptions{}),
	)
}
