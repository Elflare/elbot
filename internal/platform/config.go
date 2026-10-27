// config.go 汇总各平台配置定义，具体配置解析归各适配器。

package platform

import (
	"elbot/internal/config"
	"elbot/internal/platform/cli"
	qqonebot "elbot/internal/platform/qq-onebot"
	"elbot/internal/platform/qqofficial"
	"elbot/internal/platform/telegram"
)

func ConfigDefinitions() []config.Definition {
	return []config.Definition{
		cli.ConfigDefinition(),
		qqonebot.ConfigDefinition(),
		qqofficial.ConfigDefinition(),
		telegram.ConfigDefinition(),
	}
}
