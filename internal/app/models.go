package app

import (
	"time"

	"elbot/internal/config"
	"elbot/internal/llm/openai"
)

type defaultModelFactory struct{}

func (defaultModelFactory) Build(req ModelRequest) (ModelClients, error) {
	cfg := req.Foundation.Config
	logger := req.Foundation.Logger
	workModel := cfg.ModeModels["work"]
	provider := cfg.Providers[workModel.Provider]

	primary := openai.NewWithOptions(provider.BaseURL, provider.APIKey, provider.ExtraPayload, modelExtraPayloads(provider.ModelConfigs), appLLMRequestOptions(cfg.LLMRequest, provider.Proxy))
	primary.SetLogger(logger)
	naming := primary
	namingModel := ""
	if cfg.NamingModel.Provider != "" && cfg.NamingModel.Model != "" {
		if namingProvider, ok := cfg.Providers[cfg.NamingModel.Provider]; ok {
			naming = openai.NewWithOptions(namingProvider.BaseURL, namingProvider.APIKey, namingProvider.ExtraPayload, modelExtraPayloads(namingProvider.ModelConfigs), appLLMRequestOptions(cfg.LLMRequest, namingProvider.Proxy))
			naming.SetLogger(logger)
			namingModel = cfg.NamingModel.Model
		} else {
			logger.Warn("session naming provider not found, fallback to main model", "provider", cfg.NamingModel.Provider)
		}
	}
	req.Profiler.Mark("llm adapters")
	return ModelClients{Primary: primary, Naming: naming, NamingModel: namingModel}, nil
}

func appLLMRequestOptions(cfg config.LLMRequestConfig, proxy string) openai.RequestOptions {
	return openai.RequestOptions{
		FirstChunkTimeout: time.Duration(cfg.FirstChunkTimeoutSeconds) * time.Second,
		StreamIdleTimeout: time.Duration(cfg.StreamIdleTimeoutSeconds) * time.Second,
		MaxRetries:        cfg.MaxRetries,
		RetryInitialDelay: time.Duration(cfg.RetryInitialDelaySeconds) * time.Second,
		Proxy:             proxy,
	}
}

func modelExtraPayloads(modelConfigs map[string]config.ModelConfig) map[string]map[string]any {
	out := map[string]map[string]any{}
	for model, cfg := range modelConfigs {
		if cfg.ExtraPayload != nil {
			out[model] = cfg.ExtraPayload
		}
	}
	return out
}
