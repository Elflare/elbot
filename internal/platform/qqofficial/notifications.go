package qqofficial

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/signal"
)

func (a *Adapter) ConnectNotifications(s *signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error) {
	return s.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
		target, outputs := platform.NotificationTarget(n), []delivery.Output{delivery.Text(n.Text)}
		if target.Empty() && isGroupToolPreviewNotice(ctx, outputs) {
			return events.NotificationReceipt{}, nil
		}
		r, _, err := delivery.NewManager(a).SendTo(ctx, target, outputs)
		return events.NotificationReceipt{PlatformMessageIDs: r.PlatformMessageIDs}, err
	}, signal.ConnectOptions{})
}
