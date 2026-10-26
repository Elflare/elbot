// config_definition.go 声明平台配置字段与默认定义。

package qqofficial

import "elbot/internal/config"

// ConfigDefinition describes this platform without creating an adapter.
func ConfigDefinition() config.Definition {
	return config.Definition{
		Asset: "app.toml", Node: []string{"platform", "qqofficial"},
		New: func() any { return &Config{} }, UnknownFields: true,
	}
}
