// definition.go 声明 CLI 平台身份及通知目标规则。
package cli

import (
	"elbot/internal/events"
	"elbot/internal/platform/share"
)

func Definition() share.Definition {
	return share.Definition{
		PrivateSessionIDCode: "cl", GroupSessionIDCode: "cl", ConfigDefinition: ConfigDefinition(),
		// 本地展示不需要聊天目标；远程客户端由 CLI 的原始连接及目标定位。
		ValidateNotificationTarget: func(events.NotificationTarget) error { return nil },
	}
}
