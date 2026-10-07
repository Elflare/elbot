// Package modelmgr owns model selection, provider clients and model catalogs.
package modelmgr

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

type Options struct {
	Clients      map[string]llm.Client
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
	clients      map[string]llm.Client
	origins      map[string]llm.Origin
	providers    map[string]*providerCatalog
	retrying     *signal.Signal[ModelRetryingEvent]
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
		clients:      make(map[string]llm.Client, len(opts.Providers)),
		origins:      make(map[string]llm.Origin, len(opts.Providers)),
		providers:    make(map[string]*providerCatalog, len(opts.Providers)),
		retrying:     signal.New[ModelRetryingEvent]("model.retrying"),
	}
	for name, provider := range opts.Providers {
		client := opts.Clients[name]
		if client == nil {
			return nil, fmt.Errorf("client not found for provider %q", name)
		}
		mode := provider.EffectiveAPIMode()
		s.clients[name] = client
		s.origins[name] = llm.Origin{Provider: name, APIType: llm.APIType(mode), BaseURL: strings.TrimRight(provider.BaseURL, "/")}
		s.providers[name] = &providerCatalog{
			baseURL: provider.BaseURL, apiKey: provider.APIKey, apiKeyEnv: provider.APIKeyEnv,
			configured: append([]string(nil), provider.Models...),
		}
	}
	for provider, client := range s.clients {
		if notifier, ok := client.(llm.RetryNotifier); ok {
			notifier.SetRetryNotifier(func(ctx context.Context, event llm.RetryEvent) {
				_ = s.retrying.Emit(ctx, ModelRetryingEvent{Provider: provider, Retry: event})
			})
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

func (s *Service) ClientForProvider(provider string) llm.Client { return s.clients[provider] }

// ProviderOrigins supplies immutable configuration facts for composition. Model
// selection does not choose business routes or inspect native client interfaces.
func (s *Service) ProviderOrigins() []llm.Origin {
	origins := make([]llm.Origin, 0, len(s.origins))
	for _, origin := range s.origins {
		origins = append(origins, origin)
	}
	sort.Slice(origins, func(i, j int) bool { return origins[i].Provider < origins[j].Provider })
	return origins
}
