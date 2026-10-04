package agent

import (
	"elbot/internal/config"
)

// SetContextOptions is used by standalone agent callers; state lives in contextmgr.
func (a *Agent) SetContextOptions(cfg config.ContextConfig, metadata config.ModelMetadataConfig, providers map[string]config.ProviderConfig) {
	a.contexts.Configure(cfg, metadata, providers)
}
