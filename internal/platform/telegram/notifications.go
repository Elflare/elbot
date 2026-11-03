// notifications.go 消费 Share 通知契约并实际发送平台消息。

package telegram

import (
	"elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

func (a *Adapter) ConnectNotifications(s *signal.Signal[events.Notification, signal.Empty]) (*signal.Connection, error) {
	return share.ConnectNotifications(s, a)
}
