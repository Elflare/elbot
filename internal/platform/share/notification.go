// notification.go 定义内部通知接入契约与目标转换。

package share

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/session"
	"elbot/internal/signal"
)

// FormatToolCallNotice creates the text-only tool progress shown by messaging platforms.
// The structured stream remains unmodified for clients that can render tool cards.
func FormatToolCallNotice(call delivery.ToolCall) string {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	} else {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(arguments)); err == nil {
			arguments = compact.String()
		}
	}
	runes := []rune(arguments)
	if len(runes) > 160 {
		arguments = string(runes[:160]) + "..."
	}
	return fmt.Sprintf("[tool] 正在调用 %s：%s", call.Name, arguments)
}

// NotificationReceiver 连接通知发送端；调度归 Signal，连接由根部持有和释放。
type NotificationReceiver interface {
	ConnectNotifications(*signal.Signal[events.Notification, signal.Empty]) (*signal.Connection, error)
}

// ConnectNotifications 将通知交给发送端，连接由根部持有和释放。
func ConnectNotifications(s *signal.Signal[events.Notification, signal.Empty], sender delivery.MessageSender) (*signal.Connection, error) {
	return s.Observe(func(ctx context.Context, n events.Notification) {
		if binding, ok := session.BindingFromContext(ctx); ok && !binding.Valid() {
			return
		}
		target := NotificationTarget(n)
		_, err := sender.SendTo(ctx, target, []delivery.Output{delivery.Text(n.Text)})
		if err != nil {
			record := events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "notification_send_failed", Module: "notification", Content: fmt.Sprintf("target=%+v error=%v", n.Target, err), ContentLimit: 4096, Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)}
			if record.ResultStatus == events.ResultCanceled {
				record.Level = slog.LevelInfo
			}
			events.AsyncLog(ctx, record)
		}

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
