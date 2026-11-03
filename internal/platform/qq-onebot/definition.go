// definition.go 声明 OneBot 平台身份及通知目标规则。
package qqonebot

import (
	"elbot/internal/platform/share"
)

func Definition() share.Definition {
	return share.Definition{
		PrivateSessionIDCode: "ob", GroupSessionIDCode: "OB", ConfigDefinition: ConfigDefinition(),
		ValidateNotificationTarget: share.ValidateNotificationTarget,
	}
}
