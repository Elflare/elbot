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

func (a *Adapter) ConnectNotifications(source *signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error) {
	return source.ConnectForward(func(ctx context.Context, n events.Notification) *signal.Emission[events.NotificationReceipt] {
		logged, err := events.EmitLogAsync(ctx, events.LogRecord{Category: events.LogRuntime, Level: n.Level, EventName: "runtime_notification", Module: "notification", Type: events.ContentText, Content: n.Text, ContentLimit: 4096})
		if logged == nil {
			return signal.Resolved(signal.Result[events.NotificationReceipt]{Err: err})
		}
		return signal.MapResults(logged, func(results []signal.Result[signal.Empty]) signal.Result[events.NotificationReceipt] {
			var errs []error
			var started bool
			for _, result := range results {
				errs = append(errs, result.Err)
				started = started || result.Started
			}
			return signal.Result[events.NotificationReceipt]{Started: started, Err: errors.Join(errs...)}
		})
	}, signal.ConnectOptions{})
}
