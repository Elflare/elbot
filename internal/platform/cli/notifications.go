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

func (a *Adapter) ConnectNotifications(s *signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error) {
	return s.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
		err := a.sendTUIMessage(tuiNoticeMsg{Text: n.Text, Level: n.Level}, "[notice] "+n.Text)
		if err != nil {
			delivery.LogSendResult(ctx, share.NotificationTarget(n), delivery.Text(n.Text), events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()})
		}
		return events.NotificationReceipt{}, err
	}, signal.ConnectOptions{})
}
func (s *RemoteServer) ConnectNotifications(source *signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error) {
	return source.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
		target := share.NotificationTarget(n)
		logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
		defer func() { delivery.LogSendResult(ctx, target, delivery.Text(n.Text), logResult) }()
		clients, err := s.targetClients(ctx, target)
		if err != nil {
			logResult = events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}
			return events.NotificationReceipt{}, err
		}
		var errs []error
		for _, client := range clients {
			result, err := client.write(ctx, remoteMessage{Type: remoteMsgNotice, Text: n.Text, Level: n.Level.String()})
			logResult.Content += "\n" + result.Content
			if result.ResultStatus == events.ResultFailed || (result.ResultStatus == events.ResultCanceled && logResult.ResultStatus != events.ResultFailed) {
				logResult.ResultStatus = result.ResultStatus
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("notify cli client %s: %w", client.id, err))
				s.removeClient(client)
				_ = client.conn.CloseNow()
			}
		}
		return events.NotificationReceipt{}, errors.Join(errs...)
	}, signal.ConnectOptions{})
}
