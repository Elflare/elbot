package app

import (
	"fmt"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/llm/httpclient"
	"elbot/internal/llm/responses"
)

type defaultModelFactory struct{}

func (defaultModelFactory) Build(req ModelRequest) (ModelClients, error) {
	cfg := req.Foundation.Config
	buildClient := func(_ string, provider config.ProviderConfig) (llm.Client, error) {
		return buildModelClient(provider, cfg.LLMRequest)
	}
	clients := make(map[string]llm.Client, len(cfg.Providers))
	for name, provider := range cfg.Providers {
		client, err := buildClient(name, provider)
		if err != nil {
			return ModelClients{}, fmt.Errorf("create provider %q client: %w", name, err)
		}
		clients[name] = client
	}
	req.Profiler.Mark("llm adapters")
	return ModelClients{ByProvider: clients, BuildClient: buildClient}, nil
}

func buildModelClient(provider config.ProviderConfig, request config.LLMRequestConfig) (llm.Client, error) {
	opts := appLLMRequestOptions(request, provider.Proxy)
	switch provider.EffectiveAPIMode() {
	case "chat":
		return chatcompletions.New(provider.BaseURL, provider.APIKey, provider.ExtraPayload, modelExtraPayloads(provider.ModelConfigs), opts)
	case "response":
		return responses.New(provider.BaseURL, provider.APIKey, provider.ExtraPayload, modelExtraPayloads(provider.ModelConfigs), opts)
	default:
		return nil, fmt.Errorf("invalid api_mode %q: expected chat or response", provider.APIMode)
	}
}

func appLLMRequestOptions(cfg config.LLMRequestConfig, proxy string) httpclient.Options {
	return httpclient.Options{
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
