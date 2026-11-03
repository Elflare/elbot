// notifications.go 将宿主通知接入独立队列，并转交日志信号。
package headless

import (
	"context"
	"errors"

	"elbot/internal/events"
	"elbot/internal/signal"
)

// ValidateNotificationTarget 限定宿主通知；指定聊天目标应路由到对应平台。
func ValidateNotificationTarget(target events.NotificationTarget) error {
	if target.ID != "" {
		return errors.New("service notifications do not accept a conversation target")
	}
	return nil
}

func (a *Adapter) ConnectNotifications(source *signal.Signal[events.Notification, signal.Empty]) (*signal.Connection, error) {
	return source.Observe(func(ctx context.Context, n events.Notification) error {
		_ = events.EmitLog(ctx, events.LogRecord{Category: events.LogRuntime, Level: n.Level, EventName: "runtime_notification", Module: "notification", Content: n.Text, ContentLimit: 4096})
		return nil
	}, signal.ConnectOptions{})
}
