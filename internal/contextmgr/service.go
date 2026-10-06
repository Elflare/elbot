package contextmgr

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

type Options struct {
	Compactors CompactorResolver
	Store      storage.Store
	Models     *modelmgr.Service
	Config     config.ContextConfig
	Metadata   config.ModelMetadataConfig
	Providers  map[string]config.ProviderConfig
}

type Service struct {
	compactors     CompactorResolver
	store          storage.Store
	models         *modelmgr.Service
	loader         Loader
	mu             sync.Mutex
	usageWriteMu   sync.Mutex
	config         config.ContextConfig
	modelMetadata  config.ModelMetadataConfig
	windowResolver *WindowResolver
	lastUsage      map[string]*llm.Usage
}

func New(opts Options) *Service {
	s := &Service{compactors: opts.Compactors, store: opts.Store, models: opts.Models, loader: Loader{Store: opts.Store}, lastUsage: map[string]*llm.Usage{}}
	s.Configure(opts.Config, opts.Metadata, opts.Providers)
	return s
}

func (s *Service) Configure(cfg config.ContextConfig, metadata config.ModelMetadataConfig, providers map[string]config.ProviderConfig) {
	var clientFor ClientProvider
	if s.models != nil {
		clientFor = s.models.ClientForProvider
	}
	resolver := NewWindowResolver(metadata, providers, clientFor)
	s.mu.Lock()
	s.config, s.modelMetadata, s.windowResolver = cfg, metadata, resolver
	s.mu.Unlock()
}

func (s *Service) Load(ctx context.Context, id string) (*LoadedContext, error) {
	return s.loader.Load(ctx, id)
}

func cloneUsage(usage *llm.Usage) *llm.Usage {
	if usage == nil {
		return nil
	}
	copy := *usage
	return &copy
}

// RecordUsage preserves the latest observed usage even when persistence fails.
// Serializing writers prevents a late save from replacing a newer observation.
func (s *Service) RecordUsage(ctx context.Context, id string, usage *llm.Usage) error {
	if usage == nil || id == "" {
		return nil
	}
	usage = cloneUsage(usage)
	s.usageWriteMu.Lock()
	defer s.usageWriteMu.Unlock()
	s.mu.Lock()
	s.lastUsage[id] = usage
	s.mu.Unlock()
	if s.store == nil {
		return nil
	}
	_, err := s.store.Sessions().Mutate(ctx, id, func(row *storage.Session) error {
		if _, err := DecodeState(row.Metadata); err != nil {
			return err
		}
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		if err := fields.Set("last_usage", usage); err != nil {
			return err
		}
		row.Metadata, err = fields.Encode()
		if err == nil {
			row.UpdatedAt = storage.Now()
		}
		return err
	})
	return err
}

func (s *Service) Usage(row *storage.Session) (*llm.Usage, error) {
	if row == nil {
		return nil, nil
	}
	s.mu.Lock()
	usage := cloneUsage(s.lastUsage[row.ID])
	s.mu.Unlock()
	if usage != nil {
		return usage, nil
	}
	state, err := DecodeState(row.Metadata)
	if err != nil {
		return nil, err
	}
	// Restored snapshots must not overwrite a concurrent observation.
	return cloneUsage(state.LastUsage), nil
}

func (r *Service) Status(ctx context.Context, usage *llm.Usage, selection config.ModelSelection) string {
	r.mu.Lock()
	resolver := r.windowResolver
	metadata := r.modelMetadata
	ctxCfg := r.config
	r.mu.Unlock()

	window := 0
	if resolver != nil {
		window = resolver.Resolve(ctx, selection.Provider, selection.Model)
	}
	if window <= 0 {
		window = metadata.DefaultContextWindow
	}
	threshold := ctxCfg.CompactTriggerRatio
	if threshold == 0 {
		threshold = 0.8
	}
	var sb strings.Builder
	sb.WriteString("  ")
	sb.WriteString(FormatTokens(usage))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  context window: %d\n", window))
	if usage == nil || usage.TotalTokens <= 0 || window <= 0 {
		sb.WriteString("  context usage: unknown\n")
		sb.WriteString(fmt.Sprintf("  compact threshold: %.0f%%\n", threshold*100))
		status := "unknown"
		if !ctxCfg.CompactEnabled {
			status = "disabled"
		}
		sb.WriteString(fmt.Sprintf("  compact status: %s\n", status))
		return sb.String()
	}
	ratio := float64(usage.TotalTokens) / float64(window)
	sb.WriteString(fmt.Sprintf("  context usage: %.1f%%\n", ratio*100))
	sb.WriteString(fmt.Sprintf("  compact threshold: %.0f%%\n", threshold*100))
	status := "ok"
	if !ctxCfg.CompactEnabled {
		status = "disabled"
	} else if ratio >= threshold {
		status = "will compact before next request"
	} else if ratio >= math.Max(0, threshold-0.1) {
		status = "near threshold"
	}
	sb.WriteString(fmt.Sprintf("  compact status: %s\n", status))
	return sb.String()
}

func (r *Service) ReachedCompactThreshold(ctx context.Context, usage *llm.Usage, selection config.ModelSelection) bool {
	r.mu.Lock()
	ctxCfg := r.config
	resolver := r.windowResolver
	r.mu.Unlock()
	if !ctxCfg.CompactEnabled || usage == nil || usage.TotalTokens <= 0 || resolver == nil {
		return false
	}
	window := resolver.Resolve(ctx, selection.Provider, selection.Model)
	state := UsageState{Usage: usage, ContextWindow: window, TriggerRatio: ctxCfg.CompactTriggerRatio}
	return state.ReachedThreshold()
}
