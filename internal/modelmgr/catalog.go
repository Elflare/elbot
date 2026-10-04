package modelmgr

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type ModelOption struct {
	Index       int
	Provider    string
	Model       string
	Current     bool
	ChatCurrent bool
	WorkCurrent bool
	ModeMarks   []string
	Compact     bool
	Naming      bool
}

type ModelProviderError struct {
	Provider string
	Err      error
}
type ModelListOptions struct{ Fresh bool }
type ModelListResult struct {
	Options []ModelOption
	Errors  []ModelProviderError
}

type providerCatalog struct {
	baseURL    string
	apiKey     string
	apiKeyEnv  string
	configured []string
	mu         sync.Mutex
	loaded     bool
	models     []string
	err        error
}

func (s *Service) ModelList(query string, opts ModelListOptions) ModelListResult {
	query = strings.ToLower(strings.TrimSpace(query))
	providers := make([]string, 0, len(s.providers))
	for name := range s.providers {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	models := make([][]string, len(providers))
	errs := make([]error, len(providers))
	var wg sync.WaitGroup
	for i, name := range providers {
		wg.Add(1)
		go func() { defer wg.Done(); models[i], errs[i] = s.providerModels(name, opts.Fresh) }()
	}
	wg.Wait()
	state := s.snapshot()
	chat, work := state.mode("chat"), state.mode("work")
	compact := state.compact
	if compact.Provider == "" || compact.Model == "" {
		compact = work
	}
	result := ModelListResult{}
	index := 1
	for i, name := range providers {
		for _, model := range models[i] {
			option := ModelOption{
				Index: index, Provider: name, Model: model,
				ChatCurrent: name == chat.Provider && model == chat.Model,
				WorkCurrent: name == work.Provider && model == work.Model,
				Compact:     name == compact.Provider && model == compact.Model,
				Naming:      name == state.naming.Provider && model == state.naming.Model,
			}
			for _, mode := range []string{"chat", "work", "elwisp1", "elwisp2", "elwisp3"} {
				selected := state.mode(mode)
				if name == selected.Provider && model == selected.Model {
					option.ModeMarks = append(option.ModeMarks, mode)
				}
			}
			option.Current = len(option.ModeMarks) > 0
			index++
			if query != "" && !strings.Contains(strings.ToLower(name), query) && !strings.Contains(strings.ToLower(model), query) {
				continue
			}
			result.Options = append(result.Options, option)
		}
		if errs[i] != nil {
			result.Errors = append(result.Errors, ModelProviderError{Provider: name, Err: errs[i]})
		}
	}
	return result
}

func (s *Service) providerModels(name string, fresh bool) ([]string, error) {
	p := s.providers[name]
	// Serialize refreshes for this provider so a slower old fetch cannot replace
	// a newer result. Other providers and model selections remain independent.
	p.mu.Lock()
	defer p.mu.Unlock()
	if !fresh && p.loaded {
		return append([]string(nil), p.models...), p.err
	}
	s.mu.RLock()
	currentProvider := s.state.listProvider
	s.mu.RUnlock()
	set := make(map[string]struct{}, len(p.configured))
	var fetchErr error
	if strings.TrimSpace(p.apiKey) == "" && strings.TrimSpace(p.apiKeyEnv) != "" && strings.TrimSpace(p.baseURL) != "" {
		fetchErr = fmt.Errorf("api_key_env %q is not set", p.apiKeyEnv)
	} else if name == currentProvider || (p.baseURL != "" && p.apiKey != "") {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		fetched, err := s.clients[name].ListModels(ctx)
		cancel()
		fetchErr = err
		if err == nil {
			for _, model := range fetched {
				set[model] = struct{}{}
			}
		}
	}
	for _, model := range p.configured {
		set[model] = struct{}{}
	}
	models := make([]string, 0, len(set))
	for model := range set {
		models = append(models, model)
	}
	sort.Strings(models)
	p.loaded, p.models, p.err = true, models, fetchErr
	return append([]string(nil), models...), fetchErr
}
