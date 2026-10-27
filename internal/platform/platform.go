// platform.go 保留尚未迁移的旧运行契约与通知辅助定义。
// TODO：迁入 share 对应文件并更新调用方后删除本文件；公开消息事实已归 contracts.go。
// share/runtime_contracts.go 已定义目标 Runtime 与 MessageHandler；适配器迁移后删除旧运行契约。
// share/notification.go 已定义通知接收与目标转换；各子包调用方迁移后删除这里的旧定义。

package platform

import (
	"context"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
)

// PlatformAdapter is the interface for message platform adapters (CLI, QQ, etc.).
type PlatformAdapter interface {
	Name() string
	Run(ctx context.Context, handler PlatformHandler) error
	delivery.MessageSender
}

// PlatformHandler processes incoming messages from a platform.
type PlatformHandler interface {
	HandleMessage(ctx context.Context, text string) error
}

// Runtime is the lifecycle and send surface shared by platform adapters.
type Runtime interface {
	Name() string
	Run(ctx context.Context, handler PlatformHandler) error
	delivery.MessageSender
}

// NotificationReceiver only connects a sender; scheduling belongs to the signal.
type NotificationReceiver interface {
	ConnectNotifications(*signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error)
}

// NotificationTarget retains native reply routing when the destination matches
// the captured origin. Explicit destinations use the existing platform protocol.
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
