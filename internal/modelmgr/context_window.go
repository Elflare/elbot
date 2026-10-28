package modelmgr

import (
	"context"
	"sync"

	"elbot/internal/config"
	"elbot/internal/llm"
)

type clientProvider func(provider string) llm.Client

type windowResolver struct {
	DefaultWindow int            // Fallback when neither remote nor configured metadata gives a window.
	ManualWindows map[string]int // Configured windows keyed by provider/model.
	ClientFor     clientProvider // Resolves clients within this configuration generation.
	mu            sync.RWMutex
	cache         map[string]int
}

func (r *windowResolver) Resolve(ctx context.Context, provider, model string) int {
	key := provider + "/" + model
	if value := r.cached(key); value > 0 {
		return value
	}
	if metadataProvider, ok := r.ClientFor(provider).(llm.ModelMetadataProvider); ok {
		if metadata, err := metadataProvider.ListModelMetadata(ctx); err == nil {
			for _, item := range metadata {
				if item.ID == model && item.ContextWindow > 0 {
					return r.cacheValue(key, item.ContextWindow)
				}
			}
		}
	}
	if value := r.ManualWindows[key]; value > 0 {
		return r.cacheValue(key, value)
	}
	return r.DefaultWindow
}

func (r *windowResolver) cached(key string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cache[key]
}

func (r *windowResolver) cacheValue(key string, value int) int {
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

func newWindowResolver(metadata config.ModelMetadataConfig, providers map[string]config.ProviderConfig, clientFor clientProvider) *windowResolver {
	manual := map[string]int{}
	for providerName, provider := range providers {
		for modelName, modelCfg := range provider.ModelConfigs {
			if modelCfg.ContextWindow > 0 {
				manual[providerName+"/"+modelName] = modelCfg.ContextWindow
			}
		}
	}
	return &windowResolver{DefaultWindow: metadata.DefaultContextWindow, ManualWindows: manual, ClientFor: clientFor}
}
