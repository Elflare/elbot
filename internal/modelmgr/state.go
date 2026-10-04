package modelmgr

import "elbot/internal/config"

type runtimeState struct {
	modes        map[string]config.ModelSelection
	compact      config.ModelSelection
	naming       config.ModelSelection
	listProvider string
}

func cloneModes(modes map[string]config.ModelSelection) map[string]config.ModelSelection {
	result := make(map[string]config.ModelSelection, len(modes))
	for mode, selected := range modes {
		result[mode] = selected
	}
	return result
}

func (s *Service) snapshot() runtimeState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.state
	state.modes = cloneModes(state.modes)
	return state
}

// commit serializes writers, but leaves the published snapshot readable during
// persistence. A failed save never changes the state seen by other callers.
func (s *Service) commit(update func(*runtimeState)) error {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	next := s.snapshot()
	update(&next)
	if s.statePath != "" {
		if err := s.save(s.statePath, config.StateConfig{
			Session: s.sessionState, ModeModels: cloneModes(next.modes),
			CompactModel: next.compact, NamingModel: next.naming,
		}); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.state = next
	s.mu.Unlock()
	return nil
}
