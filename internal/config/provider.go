package config

// ProviderFile is the reloadable portion of the provider configuration file.
type ProviderFile struct {
	Providers     map[string]ProviderConfig `toml:"providers"`      // Named providers and their model settings.
	ModelMetadata ModelMetadataConfig       `toml:"model_metadata"` // Default metadata for models without a known window.
}

// LoadProviders reads only the selected provider file. Secrets use the main
// configuration directory, even when the provider file is stored elsewhere.
func LoadProviders(path, configDir string) (ProviderFile, error) {
	doc := readDocument(path, func() any { return &Config{} })
	if doc.err != nil {
		return ProviderFile{}, doc.err
	}
	cfg := doc.value.(*Config)
	cfg.applyProviderDefaults()
	if err := FirstError(cfg.ValidateProviders()); err != nil {
		return ProviderFile{}, err
	}
	if err := cfg.resolveProviderAPIKeys(configDir); err != nil {
		return ProviderFile{}, err
	}
	return ProviderFile{Providers: cfg.Providers, ModelMetadata: cfg.ModelMetadata}, nil
}

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
		if mode != "response" {
			continue
		}
		store, configured := provider.ExtraPayload["store"]
		if configured {
			if _, ok := store.(bool); !ok {
				issues = append(issues, Issue{Field: []string{"providers", name, "extra_payload", "store"}, Level: LevelError, Message: "store from provider extra_payload must be a boolean"})
			}
		}
		for model, cfg := range provider.ModelConfigs {
			store, exists := cfg.ExtraPayload["store"]
			if !exists {
				continue
			}
			field := []string{"providers", name, "model_configs", model, "extra_payload", "store"}
			if configured {
				issues = append(issues, Issue{Field: field, Level: LevelError, Message: "store from model extra_payload conflicts with provider extra_payload"})
			} else if _, ok := store.(bool); !ok {
				issues = append(issues, Issue{Field: field, Level: LevelError, Message: "store from model extra_payload must be a boolean"})
			}
		}
	}
	return issues
}
