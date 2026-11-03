// definition.go 声明 Telegram 平台身份及通知目标规则。
package telegram

import (
	"elbot/internal/platform/share"
)

func Definition() share.Definition {
	return share.Definition{
		PrivateSessionIDCode: "tg", GroupSessionIDCode: "TG", ConfigDefinition: ConfigDefinition(),
		ValidateNotificationTarget: share.ValidateNotificationTarget,
	}
}
