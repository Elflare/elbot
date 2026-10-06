package modelmgr

import (
	"fmt"
	"strconv"
	"strings"

	"elbot/internal/config"
	"elbot/internal/llm"
)

// Selection fixes both the model identity and its client for one operation.
type Selection struct {
	config.ModelSelection
	Client llm.Client
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

func (s *Service) Resolve(selected config.ModelSelection) Selection {
	client := s.clients[selected.Provider]
	return Selection{ModelSelection: selected, Client: client}
}

// ValidateSelection checks an explicit task choice without changing shared state.
func (s *Service) ValidateSelection(selected config.ModelSelection) error {
	if selected.Provider == "" || selected.Model == "" {
		return fmt.Errorf("model provider/model must both be set")
	}
	if s.clients[selected.Provider] == nil {
		return fmt.Errorf("model provider %q not found", selected.Provider)
	}
	return nil
}

func (s *Service) ResolveMode(mode string) Selection {
	s.mu.RLock()
	selected := s.state.mode(mode)
	s.mu.RUnlock()
	return s.Resolve(selected)
}

func (s *Service) ResolveCompact(fallback Selection) Selection {
	s.mu.RLock()
	selected := s.state.compact
	s.mu.RUnlock()
	if selected.Provider == "" || selected.Model == "" {
		return fallback
	}
	return s.Resolve(selected)
}

func (s *Service) ResolveNaming() NamingSelection {
	s.mu.RLock()
	naming, fallback := s.state.naming, s.state.mode("work")
	s.mu.RUnlock()
	return NamingSelection{Naming: s.Resolve(naming), Fallback: s.Resolve(fallback)}
}

func (s *Service) CurrentModelForMode(mode string) ModelOption {
	selected := s.ResolveMode(mode)
	return ModelOption{Provider: selected.Provider, Model: selected.Model}
}

func (s *Service) CurrentCompactModel(mode string) ModelOption {
	state := s.snapshot()
	selected := state.compact
	if selected.Provider == "" || selected.Model == "" {
		selected = state.mode(mode)
	}
	return ModelOption{Provider: selected.Provider, Model: selected.Model, Compact: true}
}

func (s *Service) SelectModelForMode(mode, arg string) (ModelOption, error) {
	selected, err := s.selectModelOption(arg)
	if err != nil {
		return ModelOption{}, err
	}
	return s.CommitModelForMode(mode, selected)
}

// PrepareModel resolves catalog input without changing any shared selection.
func (s *Service) PrepareModel(arg string) (ModelOption, error) {
	return s.selectModelOption(arg)
}

func (s *Service) CommitModelForMode(mode string, selected ModelOption) (ModelOption, error) {
	if err := s.ValidateSelection(config.ModelSelection{Provider: selected.Provider, Model: selected.Model}); err != nil {
		return ModelOption{}, err
	}
	err := s.commit(func(state *runtimeState) {
		state.modes[mode] = config.ModelSelection{Provider: selected.Provider, Model: selected.Model}
		state.listProvider = selected.Provider
	})
	if err != nil {
		return ModelOption{}, err
	}
	selected.Current = true
	selected.ChatCurrent = mode == "chat"
	selected.WorkCurrent = mode == "work"
	return selected, nil
}

func (s *Service) SelectCompactModel(arg string) (ModelOption, error) {
	selected, err := s.selectModelOption(arg)
	if err != nil {
		return ModelOption{}, err
	}
	err = s.commit(func(state *runtimeState) {
		state.compact = config.ModelSelection{Provider: selected.Provider, Model: selected.Model}
	})
	if err != nil {
		return ModelOption{}, err
	}
	selected.Compact = true
	return selected, nil
}

func (s *Service) SelectNamingModel(arg string) (ModelOption, error) {
	selected, err := s.selectModelOption(arg)
	if err != nil {
		return ModelOption{}, err
	}
	err = s.commit(func(state *runtimeState) {
		state.naming = config.ModelSelection{Provider: selected.Provider, Model: selected.Model}
	})
	if err != nil {
		return ModelOption{}, err
	}
	selected.Naming = true
	return selected, nil
}

func (s *Service) selectModelOption(arg string) (ModelOption, error) {
	name := strings.TrimSpace(arg)
	if name == "" {
		return ModelOption{}, fmt.Errorf("usage: /model <name or number>")
	}
	models := s.ModelList("", ModelListOptions{}).Options
	if len(models) == 0 {
		return ModelOption{}, fmt.Errorf("no models available")
	}
	if idx, err := strconv.Atoi(name); err == nil {
		if idx < 1 || idx > len(models) {
			return ModelOption{}, fmt.Errorf("model index %d out of range [1-%d]", idx, len(models))
		}
		return models[idx-1], nil
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
		return matches[0], nil
	}
	if len(matches) == 0 {
		return ModelOption{}, fmt.Errorf("model %q not found", name)
	}
	parts := make([]string, len(matches))
	for i, m := range matches {
		parts[i] = fmt.Sprintf("[%d] %s/%s", m.Index, m.Provider, m.Model)
	}
	return ModelOption{}, fmt.Errorf("ambiguous model %q, matches: %s", name, strings.Join(parts, ", "))
}
