// config.go 汇总各平台配置定义，具体配置解析归各适配器。

package platform

import (
	"elbot/internal/config"
)

func ConfigDefinitions() []config.Definition {
	var definitions []config.Definition
	for _, entry := range registrations() {
		definitions = append(definitions, entry.definition.ConfigDefinition)
	}
	return definitions
}
