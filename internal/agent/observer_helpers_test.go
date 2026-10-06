package agent

import (
	"context"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/hook"
	"elbot/internal/notification"
	"elbot/internal/notification/rules"
	"elbot/internal/signal"
)

// Unit fixtures consume facts synchronously for deterministic assertions. The
// app tests exercise the production queues, shutdown and asynchronous delivery.
type assembleObserverOptions struct {
	notifications *notification.Manager
	dispatcher    *dispatch.Router
}

func connectTestObservers(a *Agent, opts assembleObserverOptions) {
	vision := rules.NewVisionNotices(a.output)
	_, _ = a.signals.VisionFallbackUsed.Connect(func(ctx context.Context, e agentevents.VisionFallbackUsedEvent) error {
		return vision.Send(ctx, e.SessionID, e.Visible)
	}, signal.ConnectOptions{})
	_, _ = a.signals.HookFailed.Connect(func(ctx context.Context, e agentevents.HookFailedEvent) error {
		if e.Notice {
			return rules.HookFailure(context.WithoutCancel(ctx), opts.notifications, hook.Event{Point: e.Point, Platform: e.Platform}, e.Err)
		}
		return nil
	}, signal.ConnectOptions{})
	_, _ = a.signals.StatusChanged.Connect(func(ctx context.Context, e agentevents.StatusChangedEvent) error {
		if e.Display {
			return opts.dispatcher.SetRuntimeStatus(ctx, e.Snapshot)
		}
		return nil
	}, signal.ConnectOptions{})
}
