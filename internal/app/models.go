package app

import (
	"fmt"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/openai"
)

type defaultModelFactory struct{}

func (defaultModelFactory) Build(req ModelRequest) (ModelClients, error) {
	cfg := req.Foundation.Config
	logger := req.Foundation.Logger
	clients := make(map[string]llm.LLM, len(cfg.Providers))
	for name, provider := range cfg.Providers {
		client, err := openai.NewWithOptions(provider.BaseURL, provider.APIKey, provider.ExtraPayload, modelExtraPayloads(provider.ModelConfigs), appLLMRequestOptions(cfg.LLMRequest, provider.Proxy))
		if err != nil {
			return ModelClients{}, fmt.Errorf("create provider %q client: %w", name, err)
		}
		client.SetLogger(logger)
		clients[name] = client
	}
	req.Profiler.Mark("llm adapters")
	return ModelClients{ByProvider: clients}, nil
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
