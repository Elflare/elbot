// notifications.go 将通知转换为平台消息并实际投递。

package telegram

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/signal"
)

func (a *Adapter) ConnectNotifications(s *signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error) {
	return s.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
		r, _, err := delivery.NewManager(a).SendTo(ctx, platform.NotificationTarget(n), []delivery.Output{delivery.Text(n.Text)})
		return events.NotificationReceipt{PlatformMessageIDs: r.PlatformMessageIDs}, err
	}, signal.ConnectOptions{})
}
