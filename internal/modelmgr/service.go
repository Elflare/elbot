// Package modelmgr owns model selection, provider clients and model catalogs.
package modelmgr

import (
	"context"
	"fmt"
	"sync"

	"elbot/internal/config"
	"elbot/internal/llm"
)

type Options struct {
	Clients      map[string]llm.LLM
	Providers    map[string]config.ProviderConfig
	ModeModels   map[string]config.ModelSelection
	CompactModel config.ModelSelection
	NamingModel  config.ModelSelection
	StatePath    string
	DefaultMode  string
}

// Service publishes complete selection snapshots. Its provider configuration
// and clients are immutable after construction; callers own their requests.
type Service struct {
	mu           sync.RWMutex
	state        runtimeState
	commitMu     sync.Mutex
	statePath    string
	sessionState config.StateSessionConfig
	save         func(string, config.StateConfig) error
	clients      map[string]llm.LLM
	providers    map[string]*providerCatalog
}

func New(opts Options) (*Service, error) {
	if len(opts.Providers) == 0 {
		return nil, fmt.Errorf("at least one provider is required")
	}
	if err := validateSelection("mode_models.work", opts.ModeModels["work"], opts.Providers); err != nil {
		return nil, err
	}
	for mode, selected := range opts.ModeModels {
		if err := validateSelection("mode_models."+mode, selected, opts.Providers); err != nil {
			return nil, err
		}
	}
	for name, selected := range map[string]config.ModelSelection{"compact_model": opts.CompactModel, "naming_model": opts.NamingModel} {
		if selected.Provider == "" && selected.Model == "" {
			continue
		}
		if err := validateSelection(name, selected, opts.Providers); err != nil {
			return nil, err
		}
	}
	s := &Service{
		state:        runtimeState{modes: cloneModes(opts.ModeModels), compact: opts.CompactModel, naming: opts.NamingModel, listProvider: opts.ModeModels["work"].Provider},
		statePath:    opts.StatePath,
		sessionState: config.StateSessionConfig{DefaultMode: opts.DefaultMode},
		save:         config.SaveState,
		clients:      make(map[string]llm.LLM, len(opts.Providers)),
		providers:    make(map[string]*providerCatalog, len(opts.Providers)),
	}
	for name, provider := range opts.Providers {
		client := opts.Clients[name]
		if client == nil {
			return nil, fmt.Errorf("client not found for provider %q", name)
		}
		s.clients[name] = client
		s.providers[name] = &providerCatalog{
			baseURL: provider.BaseURL, apiKey: provider.APIKey, apiKeyEnv: provider.APIKeyEnv,
			configured: append([]string(nil), provider.Models...),
		}
	}
	return s, nil
}

func validateSelection(name string, selected config.ModelSelection, providers map[string]config.ProviderConfig) error {
	if selected.Provider == "" || selected.Model == "" {
		return fmt.Errorf("%s provider/model must both be set", name)
	}
	if _, ok := providers[selected.Provider]; !ok {
		return fmt.Errorf("%s provider %q not found", name, selected.Provider)
	}
	return nil
}

func (s *Service) ClientForProvider(provider string) llm.LLM { return s.clients[provider] }

// SetRetryNotifier connects the existing output integration at startup. Neither
// this callback nor provider I/O is invoked while holding selection locks.
func (s *Service) SetRetryNotifier(notify func(context.Context, string, llm.RetryEvent)) {
	for provider, client := range s.clients {
		if notifier, ok := client.(llm.RetryNotifier); ok {
			if notify == nil {
				notifier.SetRetryNotifier(nil)
				continue
			}
			notifier.SetRetryNotifier(func(ctx context.Context, event llm.RetryEvent) { notify(ctx, provider, event) })
		}
	}
}
