// notifications.go 将通知投递到对应的 CLI 展示目标。

package cli

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
	"errors"
	"fmt"
)

func (a *Adapter) ConnectNotifications(s *signal.Signal[events.Notification, signal.Empty]) (*signal.Connection, error) {
	return s.Observe(func(ctx context.Context, n events.Notification) error {
		err := a.sendTUIMessage(tuiNoticeMsg{Text: n.Text, Level: n.Level}, "[notice] "+n.Text)
		if err != nil {
			delivery.LogSendResult(ctx, share.NotificationTarget(n), delivery.Text(n.Text), err)
		}
		return err
	}, signal.ConnectOptions{})
}
func (s *RemoteServer) ConnectNotifications(source *signal.Signal[events.Notification, signal.Empty]) (*signal.Connection, error) {
	return source.Observe(func(ctx context.Context, n events.Notification) error {
		target := share.NotificationTarget(n)
		clients, err := s.targetClients(ctx, target)
		if err != nil {
			delivery.LogSendResult(ctx, target, delivery.Text(n.Text), err)
			return err
		}
		var errs []error
		for _, client := range clients {
			err := client.write(ctx, remoteMessage{Type: remoteMsgNotice, Text: n.Text, Level: n.Level.String()})
			if err != nil {
				errs = append(errs, fmt.Errorf("notify cli client %s: %w", client.id, err))
				s.removeClient(client)
				_ = client.conn.CloseNow()
			}
		}
		return errors.Join(errs...)
	}, signal.ConnectOptions{})
}
