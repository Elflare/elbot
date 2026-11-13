package modelmgr

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
)

// Selection fixes both the model identity and its client for one operation.
type Selection struct {
	config.ModelSelection
	Client    llm.Client
	Origin    llm.Origin   // Configured source paired with Client for this operation.
	providers *providerSet // Retains this operation's configuration and window caches.
}

type NamingSelection struct {
	Naming   Selection
	Fallback Selection
}

func (state runtimeState) mode(mode string) config.ModelSelection {
	selected := state.modes[mode]
	if selected.Provider == "" || selected.Model == "" {
		return state.modes["work"]
	}
	return selected
}

func (s *ModelManager) Resolve(selected config.ModelSelection) Selection {
	return s.providerSnapshot().resolve(selected)
}

func (p *providerSet) resolve(selected config.ModelSelection) Selection {
	return Selection{ModelSelection: selected, Client: p.clients[selected.Provider], Origin: p.origins[selected.Provider], providers: p}
}

// ContextWindow uses the same provider generation as the selected client.
func (s Selection) ContextWindow(ctx context.Context) int {
	if s.providers == nil {
		return config.DefaultContextWindow
	}
	return s.providers.windows.Resolve(ctx, s.Provider, s.Model)
}

// ValidateSelection checks an explicit task choice without changing shared state.
func (s *ModelManager) ValidateSelection(selected config.ModelSelection) operationresult.Result[struct{}] {
	if selected.Provider == "" || selected.Model == "" {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidArguments, Message: "model provider/model must both be set"}
	}
	if s.providerSnapshot().clients[selected.Provider] == nil {
		return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: fmt.Sprintf("model provider %q not found", selected.Provider)}
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}
}

func (s *ModelManager) ResolveMode(mode string) Selection {
	s.mu.RLock()
	selected := s.state.mode(mode)
	providers := s.providers
	s.mu.RUnlock()
	return providers.resolve(selected)
}

func (s *ModelManager) ResolveCompact(fallback Selection) Selection {
	s.mu.RLock()
	selected := s.state.compact
	s.mu.RUnlock()
	if selected.Provider == "" || selected.Model == "" {
		return fallback
	}
	return fallback.providers.resolve(selected)
}

func (s *ModelManager) ResolveNaming(ctx context.Context) NamingSelection {
	fallback, _ := contextinfo.ModelFromContext(ctx)
	s.mu.RLock()
	naming := s.state.naming
	providers := s.providers
	s.mu.RUnlock()
	return NamingSelection{Naming: providers.resolve(naming), Fallback: providers.resolve(config.ModelSelection{Provider: fallback.Provider, Model: fallback.Model})}
}

func (s *ModelManager) CurrentModelForMode(mode string) ModelOption {
	selected := s.ResolveMode(mode)
	return ModelOption{Provider: selected.Provider, Model: selected.Model}
}

func (s *ModelManager) CurrentCompactModel(mode string) ModelOption {
	state := s.snapshot()
	selected := state.compact
	if selected.Provider == "" || selected.Model == "" {
		selected = state.mode(mode)
	}
	return ModelOption{Provider: selected.Provider, Model: selected.Model, Compact: true}
}

// PrepareModel resolves catalog input without changing any shared selection.
func (s *ModelManager) PrepareModel(arg string) (operationresult.Result[ModelOption], error) {
	return s.selectModelOption(arg)
}

func (s *ModelManager) CommitModelForMode(mode string, selected ModelOption) (operationresult.Result[ModelOption], error) {
	if result := s.ValidateSelection(config.ModelSelection{Provider: selected.Provider, Model: selected.Model}); result.Status != operationresult.Success {
		return operationresult.Result[ModelOption]{Status: result.Status, Message: result.Message}, nil
	}
	err := s.commit(func(state *runtimeState) {
		state.modes[mode] = config.ModelSelection{Provider: selected.Provider, Model: selected.Model}
		state.listProvider = selected.Provider
	})
	if err != nil {
		return operationresult.Result[ModelOption]{}, err
	}
	return operationresult.Result[ModelOption]{Status: operationresult.Success, Data: selected}, nil
}

func (s *ModelManager) SelectCompactModel(arg string) (operationresult.Result[ModelOption], error) {
	result, err := s.selectModelOption(arg)
	if err != nil || result.Status != operationresult.Success {
		return result, err
	}
	selected := result.Data
	err = s.commit(func(state *runtimeState) {
		state.compact = config.ModelSelection{Provider: selected.Provider, Model: selected.Model}
	})
	if err != nil {
		return operationresult.Result[ModelOption]{}, err
	}
	selected.Compact = true
	return operationresult.Result[ModelOption]{Status: operationresult.Success, Data: selected}, nil
}

func (s *ModelManager) SelectNamingModel(arg string) (operationresult.Result[ModelOption], error) {
	result, err := s.selectModelOption(arg)
	if err != nil || result.Status != operationresult.Success {
		return result, err
	}
	selected := result.Data
	err = s.commit(func(state *runtimeState) {
		state.naming = config.ModelSelection{Provider: selected.Provider, Model: selected.Model}
	})
	if err != nil {
		return operationresult.Result[ModelOption]{}, err
	}
	selected.Naming = true
	return operationresult.Result[ModelOption]{Status: operationresult.Success, Data: selected}, nil
}

func (s *ModelManager) selectModelOption(arg string) (operationresult.Result[ModelOption], error) {
	name := strings.TrimSpace(arg)
	if name == "" {
		return operationresult.Result[ModelOption]{Status: operationresult.InvalidArguments, Message: "usage: /model <name or number>"}, nil
	}
	catalog := s.ModelList("", ModelListOptions{})
	models := catalog.Options
	if len(models) == 0 {
		for _, failure := range catalog.Errors {
			if failure.Err != nil {
				return operationresult.Result[ModelOption]{}, fmt.Errorf("load model catalog %s: %w", failure.Provider, failure.Err)
			}
		}
		return operationresult.Result[ModelOption]{Status: operationresult.Unavailable, Message: "no models available"}, nil
	}
	if idx, err := strconv.Atoi(name); err == nil {
		if idx < 1 || idx > len(models) {
			return operationresult.Result[ModelOption]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("model index %d out of range [1-%d]", idx, len(models))}, nil
		}
		return operationresult.Result[ModelOption]{Status: operationresult.Success, Data: models[idx-1]}, nil
	}
	var matches []ModelOption
	for _, m := range models {
		if strings.EqualFold(m.Model, name) || strings.EqualFold(m.Provider+"/"+m.Model, name) {
			matches = append(matches, m)
		}
	}
	if len(matches) == 0 {
		query := strings.ToLower(name)
		for _, m := range models {
			if strings.Contains(strings.ToLower(m.Model), query) || strings.Contains(strings.ToLower(m.Provider), query) {
				matches = append(matches, m)
			}
		}
	}
	if len(matches) == 1 {
		return operationresult.Result[ModelOption]{Status: operationresult.Success, Data: matches[0]}, nil
	}
	if len(matches) == 0 {
		return operationresult.Result[ModelOption]{Status: operationresult.NotFound, Message: fmt.Sprintf("model %q not found", name)}, nil
	}
	parts := make([]string, len(matches))
	for i, m := range matches {
		parts[i] = fmt.Sprintf("[%d] %s/%s", m.Index, m.Provider, m.Model)
	}
	return operationresult.Result[ModelOption]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("ambiguous model %q, matches: %s", name, strings.Join(parts, ", "))}, nil
}
