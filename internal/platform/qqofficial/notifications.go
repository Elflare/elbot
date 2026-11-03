// notifications.go 转换通知目标并调用平台发送，连接由上层持有和释放。

package qqofficial

import (
	"elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

func (a *Adapter) ConnectNotifications(s *signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error) {
	return share.ConnectNotifications(s, a)
}
