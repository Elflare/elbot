package rules

import "elbot/internal/config"

// ConfigDefinition checks user rules without constructing Hook runtimes.
func ConfigDefinition() config.Definition {
	return config.Definition{
		Asset: "plugins/hooks.toml",
		New:   func() any { return &Config{} },
		Rules: []config.Rule{
			{Path: []string{"rules"}, Example: true},
			{Path: []string{"plugins"}, Example: true},
		},
	}
}
