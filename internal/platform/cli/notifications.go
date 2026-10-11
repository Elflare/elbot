package cli

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/signal"
	"errors"
	"fmt"
)

func (a *Adapter) ConnectNotifications(s *signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error) {
	return s.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
		err := a.sendTUIMessage(tuiNoticeMsg{Text: n.Text, Level: n.Level}, "[notice] "+n.Text)
		return events.NotificationReceipt{}, err
	}, signal.ConnectOptions{})
}
func (s *RemoteServer) ConnectNotifications(source *signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error) {
	return source.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
		clients, err := s.targetClients(ctx, platform.NotificationTarget(n))
		if err != nil {
			return events.NotificationReceipt{}, err
		}
		var errs []error
		for _, client := range clients {
			_, err := client.write(ctx, remoteMessage{Type: remoteMsgNotice, Text: n.Text, Level: n.Level.String()})
			if err != nil {
				errs = append(errs, fmt.Errorf("notify cli client %s: %w", client.id, err))
				s.removeClient(client)
				_ = client.conn.CloseNow()
			}
		}
		return events.NotificationReceipt{}, errors.Join(errs...)
	}, signal.ConnectOptions{})
}
