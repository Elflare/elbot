package config

// EffectiveAPIMode also supports programmatically constructed providers.
func (p ProviderConfig) EffectiveAPIMode() string {
	if p.APIMode == "" {
		return "chat"
	}
	return p.APIMode
}

// ValidateProviders is shared by startup and read-only configuration inspection.
func (c *Config) ValidateProviders() []Issue {
	var issues []Issue
	for name, provider := range c.Providers {
		mode := provider.EffectiveAPIMode()
		if mode != "chat" && mode != "response" {
			issues = append(issues, Issue{Field: []string{"providers", name, "api_mode"}, Level: LevelError, Message: "api_mode 必须为 chat 或 response，当前为 " + mode + "。"})
		}
	}
	return issues
}
