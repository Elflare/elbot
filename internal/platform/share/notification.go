// notification.go 定义内部通知接入契约与目标转换。

package share

import (
	"errors"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
)

// NotificationReceiver 连接通知发送端；调度归 Signal，连接由根部持有和释放。
type NotificationReceiver interface {
	ConnectNotifications(*signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error)
}

// ValidateNotificationTarget 是网络平台共用的显式聊天目标规则。
func ValidateNotificationTarget(target events.NotificationTarget) error {
	if target.ID == "" {
		return errors.New("notification target ID is empty")
	}
	switch target.Kind {
	case contextinfo.ConversationPrivate, contextinfo.ConversationGroup, contextinfo.ConversationChannel:
		return nil
	default:
		return errors.New("notification conversation kind is invalid")
	}
}

// NotificationTarget 在目标匹配来源时保留原生回复路由，否则转换为显式目标。
func NotificationTarget(n events.Notification) delivery.Target {
	if n.Origin != nil {
		origin := n.Origin.Source
		kind, id := origin.ConversationKind, origin.ConversationID
		if n.Target.Platform == origin.Platform && n.Target.ID == id && n.Target.Kind == kind && (n.Target.ScopeID == "" || n.Target.ScopeID == origin.ScopeID) {
			return delivery.Target{}
		}
	}
	t := delivery.Target{Platform: n.Target.Platform, ScopeID: n.Target.ScopeID}
	if n.Target.Kind == contextinfo.ConversationGroup || n.Target.Kind == contextinfo.ConversationChannel {
		t.GroupID = n.Target.ID
	} else {
		t.PrivateUserID = n.Target.ID
	}
	return t
}
