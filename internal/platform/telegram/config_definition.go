package telegram

import "elbot/internal/config"

// ConfigDefinition describes this platform without creating an adapter.
func ConfigDefinition() config.Definition {
	return config.Definition{
		Asset: "app.toml", Node: []string{"platform", "telegram"},
		New: func() any { return &Config{} }, UnknownFields: true,
	}
}
