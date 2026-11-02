// definition.go 声明 QQ Official 平台身份及通知目标规则。
package qqofficial

import "elbot/internal/platform/share"

func Definition() share.Definition {
	return share.Definition{
		Name: platformName, PrivateSessionIDCode: "qq", GroupSessionIDCode: "QQ", ConfigDefinition: ConfigDefinition(),
		ValidateNotificationTarget: share.ValidateNotificationTarget,
	}
}
