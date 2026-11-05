package modelmgr

import (
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/llm/httpclient"
	"elbot/internal/llm/responses"
)

func buildModelClient(provider config.ProviderConfig, request config.LLMRequestConfig) (llm.Client, error) {
	opts, retryOptions := requestOptions(request, provider.Proxy)
	if provider.EffectiveAPIMode() == "response" {
		return responses.New(provider.BaseURL, provider.APIKey, provider.ExtraPayload, modelExtraPayloads(provider.ModelConfigs), responses.RequestOptions{Options: opts, RetryOptions: retryOptions})
	}
	return chatcompletions.New(provider.BaseURL, provider.APIKey, provider.ExtraPayload, modelExtraPayloads(provider.ModelConfigs), chatcompletions.RequestOptions{Options: opts, RetryOptions: retryOptions})
}

func requestOptions(cfg config.LLMRequestConfig, proxy string) (httpclient.Options, llm.RetryOptions) {
	return httpclient.Options{
			FirstChunkTimeout: time.Duration(cfg.FirstChunkTimeoutSeconds) * time.Second,
			StreamIdleTimeout: time.Duration(cfg.StreamIdleTimeoutSeconds) * time.Second,
			Proxy:             proxy,
		}, llm.RetryOptions{
			MaxRetries:        cfg.MaxRetries,
			RetryInitialDelay: time.Duration(cfg.RetryInitialDelaySeconds) * time.Second,
		}
}

func modelExtraPayloads(modelConfigs map[string]config.ModelConfig) map[string]map[string]any {
	out := make(map[string]map[string]any)
	for model, cfg := range modelConfigs {
		out[model] = cfg.ExtraPayload
	}
	return out
}
