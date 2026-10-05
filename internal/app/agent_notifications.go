package app

import (
	"context"
	"errors"
	"log/slog"

	"elbot/internal/agent"
	"elbot/internal/hook"
	"elbot/internal/notification"
	"elbot/internal/notification/rules"
	"elbot/internal/signal"
)

func (b *signalBindings) connectAgentNotifications(events agent.Signals, notices *notification.Manager, sender rules.AssistantSender, logger *slog.Logger) error {
	progress, err := b.newQueue("agent.progress_notices", logger, false)
	if err != nil {
		return err
	}
	failures, err := b.newQueue("agent.failure_notices", logger, false)
	if err != nil {
		return err
	}
	vision := rules.NewVisionNotices(sender)
	return errors.Join(
		connectSignal(b, events.VisionFallbackUsed, func(ctx context.Context, e agent.VisionFallbackUsedEvent) error {
			return vision.Send(ctx, e.SessionID, e.Visible)
		}, signal.ConnectOptions{Executor: progress, Lifetime: signal.FollowEmit, Shutdown: signal.CancelPending}),
		connectSignal(b, events.HookFailed, func(ctx context.Context, e agent.HookFailedEvent) error {
			if !e.Notice {
				return nil
			}
			return rules.HookFailure(ctx, notices, hook.Event{Point: e.Point, Platform: e.Platform, Session: hook.SessionContext{ID: e.SessionID}}, e.Err)
		}, signal.ConnectOptions{Executor: failures, Lifetime: signal.FollowExecutor, Shutdown: signal.CancelPending}),
	)
}
