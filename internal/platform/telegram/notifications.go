// notifications.go 消费 Share 通知契约并实际发送平台消息。

package telegram

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

func (a *Adapter) ConnectNotifications(s *signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error) {
	return s.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
		r, _, err := delivery.NewManager(a).SendTo(ctx, share.NotificationTarget(n), []delivery.Output{delivery.Text(n.Text)})
		return events.NotificationReceipt{PlatformMessageIDs: r.PlatformMessageIDs}, err
	}, signal.ConnectOptions{})
}
