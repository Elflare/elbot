package agent

import (
	"context"
	"log/slog"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/notification/rules"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
)

// Unit fixtures consume facts synchronously for deterministic assertions. The
// app tests exercise the production queues, shutdown and asynchronous delivery.
type assembleObserverOptions struct {
	display interface {
		SetRuntimeStatus(context.Context, runtimestatus.Snapshot) error
	}
}

func connectTestObservers(a *agentFixture, opts assembleObserverOptions) {
	vision := rules.NewVisionNotices()
	_, _ = a.signals.VisionFallbackUsed.Observe(func(ctx context.Context, e agentevents.VisionFallbackUsedEvent) error {
		return vision.Send(ctx, e.SessionID, e.Visible)
	}, signal.ConnectOptions{})
	_, _ = a.signals.HookFailed.Observe(func(ctx context.Context, e agentevents.HookFailedEvent) error {
		if e.Notice {
			text := rules.HookFailure(hook.Event{Point: e.Point, Platform: e.Platform}, e.Err)
			if text != "" {
				_, err := events.NotificationRequested.Emit(context.WithoutCancel(ctx), events.NewNotification(ctx, slog.LevelError, text))
				return err
			}
		}
		return nil
	}, signal.ConnectOptions{})
	_, _ = a.signals.StatusChanged.Observe(func(ctx context.Context, e agentevents.StatusChangedEvent) error {
		if msg, ok := platform.MessageContextFrom(ctx); ok {
			if suppressed, ok := msg.Sender.(interface{ NotificationsSuppressed() bool }); ok && suppressed.NotificationsSuppressed() {
				return nil
			}
		}
		if opts.display != nil {
			return opts.display.SetRuntimeStatus(ctx, e.Snapshot)
		}
		return nil
	}, signal.ConnectOptions{})
}
