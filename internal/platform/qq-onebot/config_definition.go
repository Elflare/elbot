// config_definition.go 注册 OneBot 配置元数据，通过 Config 提供字段定义。
// 不创建适配器，配置默认值与依赖构造保留在 adapter.go。

package qqonebot

import "elbot/internal/config"

// ConfigDefinition describes this platform without creating an adapter.
func ConfigDefinition() config.Definition {
	return config.Definition{
		Asset: "app.toml", Node: []string{"platform", "qqonebot"},
		New: func() any { return &Config{} }, UnknownFields: true,
	}
}
