package contextmgr

import (
	"context"
	"sync"

	"elbot/internal/config"
	"elbot/internal/llm"
)

type ClientProvider func(provider string) llm.LLM

type WindowResolver struct {
	DefaultWindow int
	ManualWindows map[string]int
	ClientFor     ClientProvider
	mu            sync.RWMutex
	cache         map[string]int
}

func (r *WindowResolver) Resolve(ctx context.Context, provider, model string) int {
	key := provider + "/" + model
	if value := r.cached(key); value > 0 {
		return value
	}
	if r.ClientFor != nil {
		if metadataProvider, ok := r.ClientFor(provider).(llm.ModelMetadataProvider); ok {
			if metadata, err := metadataProvider.ListModelMetadata(ctx); err == nil {
				for _, item := range metadata {
					if item.ID == model && item.ContextWindow > 0 {
						return r.cacheValue(key, item.ContextWindow)
					}
				}
			}
		}
	}
	if value := r.ManualWindows[key]; value > 0 {
		return r.cacheValue(key, value)
	}
	if r.DefaultWindow > 0 {
		return r.DefaultWindow
	}
	return config.DefaultContextWindow
}

func (r *WindowResolver) cached(key string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cache[key]
}

func (r *WindowResolver) cacheValue(key string, value int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cached := r.cache[key]; cached > 0 {
		return cached
	}
	if r.cache == nil {
		r.cache = map[string]int{}
	}
	r.cache[key] = value
	return value
}

func NewWindowResolver(metadata config.ModelMetadataConfig, providers map[string]config.ProviderConfig, clientFor ClientProvider) *WindowResolver {
	manual := map[string]int{}
	for providerName, provider := range providers {
		for modelName, modelCfg := range provider.ModelConfigs {
			if modelCfg.ContextWindow > 0 {
				manual[providerName+"/"+modelName] = modelCfg.ContextWindow
			}
		}
	}
	return &WindowResolver{DefaultWindow: metadata.DefaultContextWindow, ManualWindows: manual, ClientFor: clientFor}
}
