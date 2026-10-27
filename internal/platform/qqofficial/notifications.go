// notifications.go 转换通知目标并调用平台发送，连接由上层持有和释放。
// TODO：改用 Share 通知契约，移除对 Platform 根包的依赖。

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
		r, _, err := delivery.NewManager(a).SendTo(ctx, target, outputs)
		return events.NotificationReceipt{PlatformMessageIDs: r.PlatformMessageIDs}, err
	}, signal.ConnectOptions{})
}
