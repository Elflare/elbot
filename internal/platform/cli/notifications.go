// notifications.go 统一订阅 CLI 通知并委托根部持有的实现呈现。
package cli

import (
	"context"
	"fmt"
	"log/slog"

	"elbot/internal/events"
	clishare "elbot/internal/platform/cli/share"
	platformshare "elbot/internal/platform/share"
	"elbot/internal/session"
	"elbot/internal/signal"
)

func (a *Adapter) ConnectNotifications(source *signal.Signal[events.Notification, signal.Empty]) (*signal.Connection, error) {
	return source.Observe(func(ctx context.Context, notification events.Notification) {
		if binding, ok := session.BindingFromContext(ctx); ok && !binding.Valid() {
			return
		}
		err := a.runtime.PresentNotice(ctx, clishare.Notice{
			Text: notification.Text, Level: notification.Level.String(),
			Target:      notification.Target,
			Destination: platformshare.NotificationTarget(notification),
		})
		if err == nil {
			return
		}
		level := slog.LevelWarn
		if events.LogStatus(err) == events.ResultCanceled {
			level = slog.LevelInfo
		}
		events.AsyncLog(ctx, events.LogRecord{
			Category: events.LogRuntime, Level: level, EventName: "notification_send_failed", Module: "cli",
			Content: fmt.Sprintf("target=%+v error=%v", notification.Target, err), ContentLimit: 4096,
			Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err),
		})
	}, signal.ConnectOptions{})
}
