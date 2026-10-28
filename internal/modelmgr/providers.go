package modelmgr

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
)

// providerSet owns one configuration generation, including its isolated caches.
// Published maps are immutable; only the catalog and window caches are mutable.
type providerSet struct {
	clients  map[string]llm.Client
	origins  map[string]llm.Origin
	catalogs map[string]*providerCatalog
	windows  *windowResolver
}

func (s *ModelManager) providerSnapshot() *providerSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providers
}

func (s *ModelManager) newProviderSet(ctx context.Context, file config.ProviderFile) (*providerSet, error) {
	p := &providerSet{
		clients:  make(map[string]llm.Client, len(file.Providers)),
		origins:  make(map[string]llm.Origin, len(file.Providers)),
		catalogs: make(map[string]*providerCatalog, len(file.Providers)),
	}
	for name, provider := range file.Providers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		client, err := buildModelClient(provider, s.request)
		if err != nil {
			return nil, fmt.Errorf("create provider %q client: %w", name, err)
		}
		p.clients[name] = client
		p.origins[name] = llm.Origin{Provider: name, APIType: llm.APIType(provider.EffectiveAPIMode()), BaseURL: strings.TrimRight(provider.BaseURL, "/")}
		p.catalogs[name] = &providerCatalog{baseURL: provider.BaseURL, apiKey: provider.APIKey, apiKeyEnv: provider.APIKeyEnv, configured: append([]string(nil), provider.Models...)}
		s.bindRetryNotifier(name, client)
	}
	p.windows = newWindowResolver(file.ModelMetadata, file.Providers, func(name string) llm.Client { return p.clients[name] })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p, nil
}

// ReloadProviders prepares a complete replacement before publication. Existing
// selections and network requests continue to own the previous generation.
func (s *ModelManager) ReloadProviders(ctx context.Context) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := config.LoadProviders(s.providersPath, s.configDir)
	if err != nil {
		return err
	}
	next, err := s.newProviderSet(ctx, file)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.providers = next
	s.mu.Unlock()
	return nil
}

func (s *ModelManager) bindRetryNotifier(provider string, client llm.Client) {
	if notifier, ok := client.(llm.RetryNotifier); ok {
		notifier.SetRetryNotifier(func(ctx context.Context, event llm.RetryEvent) {
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			record := ModelRetryingEvent{Provider: provider, Retry: event, CallContext: ctx}
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			_, _ = s.retrying.Emit(context.WithoutCancel(ctx), record)
		})
	}
}
