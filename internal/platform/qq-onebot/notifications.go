// notifications.go 负责通知目标转换与实际消息投递。
// TODO：改用 share.NotificationTarget 和 Share 通知契约，删除对 Platform 根包的依赖。

package qqonebot

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
		r, _, err := delivery.NewManager(a).SendTo(ctx, target, outputs)
		return events.NotificationReceipt{PlatformMessageIDs: r.PlatformMessageIDs}, err
	}, signal.ConnectOptions{})
}
