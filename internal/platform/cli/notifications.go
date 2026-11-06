// notifications.go 将通知投递到对应的 CLI 展示目标。

package cli

// TODO(结构迁移): 统一通知订阅留根包，分别调用 local/ 展示与 remote/ 投递。

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/session"
	"elbot/internal/signal"
	"fmt"
	"log/slog"
)

func (a *Adapter) ConnectNotifications(s *signal.Signal[events.Notification, signal.Empty]) (*signal.Connection, error) {
	return s.Observe(func(ctx context.Context, n events.Notification) {
		if binding, ok := session.BindingFromContext(ctx); ok && !binding.Valid() {
			return
		}
		err := a.sendTUIMessage(tuiNoticeMsg{Text: n.Text, Level: n.Level}, "[notice] "+n.Text)
		if err != nil {
			record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "notification_send_failed", Module: "cli", Content: fmt.Sprintf("target=%+v operation=tui_notice error=%v", n.Target, err), ContentLimit: 4096, Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)}
			if record.ResultStatus == events.ResultCanceled {
				record.Level = slog.LevelInfo
			}
			events.AsyncLog(ctx, record)
		}

	}, signal.ConnectOptions{})
}
func (s *RemoteServer) ConnectNotifications(source *signal.Signal[events.Notification, signal.Empty]) (*signal.Connection, error) {
	return source.Observe(func(ctx context.Context, n events.Notification) {
		if binding, ok := session.BindingFromContext(ctx); ok && !binding.Valid() {
			return
		}
		target := share.NotificationTarget(n)
		clients, err := s.targetClients(ctx, target)
		if err != nil {
			events.AsyncLog(ctx, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "notification_rejected", Module: "cli", Content: fmt.Sprintf("target=%+v error=%v", n.Target, err), ContentLimit: 4096, Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.ResultFailed})
			return
		}
		for _, client := range clients {
			err := client.write(ctx, remoteMessage{Type: remoteMsgNotice, Text: n.Text, Level: n.Level.String()})
			if err != nil {
				record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "notification_send_failed", Module: "cli", Content: fmt.Sprintf("client_id=%s operation=%s error=%v", client.id, remoteMsgNotice, err), ContentLimit: 4096, Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)}
				if record.ResultStatus == events.ResultCanceled {
					record.Level = slog.LevelInfo
				}
				events.AsyncLog(ctx, record)
				s.removeClient(client)
				_ = client.conn.CloseNow()
			}
		}

	}, signal.ConnectOptions{})
}
