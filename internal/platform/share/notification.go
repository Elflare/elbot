// notification.go 定义内部通知接入契约与目标转换。

package share

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
)

// NotificationReceiver 连接通知发送端；调度归 Signal，连接由根部持有和释放。
type NotificationReceiver interface {
	ConnectNotifications(*signal.Signal[events.Notification, signal.Empty]) (*signal.Connection, error)
}

// ConnectNotifications 将通知交给发送端，连接由根部持有和释放。
func ConnectNotifications(s *signal.Signal[events.Notification, signal.Empty], sender delivery.MessageSender) (*signal.Connection, error) {
	return s.Observe(func(ctx context.Context, n events.Notification) error {
		target := NotificationTarget(n)
		_, err := sender.SendTo(ctx, target, []delivery.Output{delivery.Text(n.Text)})
		if err != nil {
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "notification_send_failed", Module: "notification", Content: fmt.Sprintf("target=%+v error=%v", n.Target, err), ContentLimit: 4096, Identity: events.LogIdentityFromExecution(facts), ResultStatus: events.LogStatus(err)}
			if record.ResultStatus == events.ResultCanceled {
				record.Level = slog.LevelInfo
			}
			events.AsyncLog(ctx, record)
		}
		return nil
	}, signal.ConnectOptions{})
}

// ValidateNotificationTarget 是网络平台共用的显式聊天目标规则。
func ValidateNotificationTarget(target events.NotificationTarget) error {
	if target.ID == "" {
		return errors.New("notification target ID is empty")
	}
	switch target.ChatType {
	case contextinfo.ChatPrivate, contextinfo.ChatGroup, contextinfo.ChatChannel:
		return nil
	default:
		return errors.New("notification conversation kind is invalid")
	}
}

// NotificationTarget 在目标匹配来源时保留原生回复路由，否则转换为显式目标。
func NotificationTarget(n events.Notification) delivery.Target {
	if n.Origin != nil {
		origin := n.Origin.Source
		kind, id := origin.ChatType, origin.ChatID
		if n.Target.Platform == origin.Platform && n.Target.ID == id && n.Target.ChatType == kind {
			return delivery.Target{}
		}
	}
	return delivery.Target{Platform: n.Target.Platform, ChatType: n.Target.ChatType, ChatID: n.Target.ID}
}
