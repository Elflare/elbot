// config.go 汇总 Hook 配置定义，不创建插件运行时。

package hook

import (
	"elbot/internal/config"
	"elbot/internal/hook/rules"
)

// ConfigDefinitions 提供 Hook 内部模块的只读配置检查定义。
func ConfigDefinitions() []config.Definition {
	return []config.Definition{rules.ConfigDefinition()}
}
