// notification.go 定义内部通知接入契约与目标转换。

package share

import (
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
)

// NotificationReceiver 连接通知发送端；调度归 Signal，连接由根部持有和释放。
type NotificationReceiver interface {
	ConnectNotifications(*signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error)
}

// NotificationTarget 在目标匹配来源时保留原生回复路由，否则转换为显式目标。
func NotificationTarget(n events.Notification) delivery.Target {
	if n.Origin != nil {
		origin := n.Origin.Source
		kind, id := origin.ConversationKind, origin.ConversationID
		if k, v, ok := strings.Cut(origin.ScopeID, ":"); ok {
			if kind == "" || kind == contextinfo.ConversationUnknown {
				kind = contextinfo.ConversationKind(k)
			}
			if id == "" {
				id = v
			}
		}
		if n.Target.Platform == origin.Platform && n.Target.ID == id && n.Target.Kind == kind && (n.Target.ScopeID == "" || n.Target.ScopeID == origin.ScopeID) {
			return delivery.Target{}
		}
	}
	t := delivery.Target{Platform: n.Target.Platform, ScopeID: n.Target.ScopeID}
	if n.Target.Kind == contextinfo.ConversationPrivate {
		t.PrivateUserID = n.Target.ID
	} else {
		t.GroupID = n.Target.ID
	}
	return t
}
