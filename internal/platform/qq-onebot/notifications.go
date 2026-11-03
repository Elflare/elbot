// notifications.go 负责通知目标转换与实际消息投递。

package qqonebot

import (
	"elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

func (a *Adapter) ConnectNotifications(s *signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error) {
	return share.ConnectNotifications(s, a)
}
