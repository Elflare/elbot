// config_definition.go 声明 CLI 配置字段与默认定义。

package cli

import "elbot/internal/config"

// ConfigDefinition describes this platform without creating an adapter.
func ConfigDefinition() config.Definition {
	return config.Definition{
		Asset: "app.toml", Node: []string{"platform", "cli"},
		New: func() any { return &Config{} }, UnknownFields: true,
		Rules: []config.Rule{
			{Path: []string{"server", "tokens"}, Example: true},
			{Path: []string{"clients"}, Example: true},
		},
	}
}
