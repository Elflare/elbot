// Package modelmgr owns model selection, provider clients and model catalogs.
package modelmgr

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

type Options struct {
	Clients       map[string]llm.Client
	Providers     map[string]config.ProviderConfig
	ModeModels    map[string]config.ModelSelection
	CompactModel  config.ModelSelection
	NamingModel   config.ModelSelection
	StatePath     string
	DefaultMode   string
	Metadata      config.ModelMetadataConfig                              // Initial model-window defaults.
	LoadProviders func(context.Context) (config.ProviderFile, error)      // Reads and validates only the reloadable configuration.
	BuildClient   func(string, config.ProviderConfig) (llm.Client, error) // Creates a new client for the named provider.
}

// Service publishes complete selection snapshots. Reloading replaces provider
// data as a unit; callers retain their snapshot for the lifetime of an operation.
type Service struct {
	mu             sync.RWMutex
	state          runtimeState
	commitMu       sync.Mutex
	statePath      string
	sessionState   config.StateSessionConfig
	save           func(string, config.StateConfig) error
	providers      *providerSet
	reloadMu       sync.Mutex
	loadProviders  func(context.Context) (config.ProviderFile, error)
	buildClient    func(string, config.ProviderConfig) (llm.Client, error)
	retrying       *signal.Signal[ModelRetryingEvent, signal.Empty]
	logConnections []*signal.Connection
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
		state:         runtimeState{modes: cloneModes(opts.ModeModels), compact: opts.CompactModel, naming: opts.NamingModel, listProvider: opts.ModeModels["work"].Provider},
		statePath:     opts.StatePath,
		sessionState:  config.StateSessionConfig{DefaultMode: opts.DefaultMode},
		save:          config.SaveState,
		loadProviders: opts.LoadProviders,
		buildClient:   opts.BuildClient,
		retrying:      signal.New[ModelRetryingEvent, signal.Empty]("model.retrying"),
	}
	var err error
	s.providers, err = s.newProviderSet(config.ProviderFile{Providers: opts.Providers, ModelMetadata: opts.Metadata}, opts.Clients)
	if err != nil {
		return nil, err
	}
	if err := s.connectLogSignals(); err != nil {
		return nil, err
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

func (s *Service) ClientForProvider(provider string) llm.Client {
	return s.providerSnapshot().clients[provider]
}

// ProviderOrigins returns independent source facts from the current generation.
// Operations use Selection.Origin to keep these facts paired with their client.
func (s *Service) ProviderOrigins() []llm.Origin {
	snapshot := s.providerSnapshot()
	origins := make([]llm.Origin, 0, len(snapshot.origins))
	for _, origin := range snapshot.origins {
		origins = append(origins, origin)
	}
	sort.Slice(origins, func(i, j int) bool { return origins[i].Provider < origins[j].Provider })
	return origins
}

// Close stops this instance's retry signal before releasing its projections.
func (s *Service) Close(ctx context.Context) error {
	if err := s.retrying.Close(ctx); err != nil {
		return err
	}
	for _, connection := range s.logConnections {
		connection.Disconnect()
	}
	return nil
}
