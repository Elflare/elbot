package routes

import (
	"fmt"
	"sync"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
)

type Registry struct {
	mu     sync.RWMutex
	sealed bool
	routes map[llm.ProtocolID]Route
}

func New() *Registry { return &Registry{routes: make(map[llm.ProtocolID]Route)} }
func (r *Registry) Register(route Route) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("protocol routes are sealed")
	}
	if route.Protocol == "" || route.Loop == nil {
		return fmt.Errorf("protocol route requires an identity and loop")
	}
	if _, ok := r.routes[route.Protocol]; ok {
		return fmt.Errorf("protocol %q already registered", route.Protocol)
	}
	r.routes[route.Protocol] = route
	return nil
}
func (r *Registry) Seal() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("protocol routes are already sealed")
	}
	if len(r.routes) == 0 {
		return fmt.Errorf("no protocol routes registered")
	}
	r.sealed = true
	return nil
}
func (r *Registry) LoopFor(id llm.ProtocolID) (dialogue.Loop, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.sealed {
		return nil, fmt.Errorf("protocol routes are not sealed")
	}
	route, ok := r.routes[id]
	if !ok {
		return nil, fmt.Errorf("protocol %q is not registered", id)
	}
	return route.Loop, nil
}
func (r *Registry) CompactorFor(id llm.ProtocolID) (contextmgr.Compactor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.sealed {
		return nil, fmt.Errorf("protocol routes are not sealed")
	}
	route, ok := r.routes[id]
	if !ok {
		return nil, fmt.Errorf("protocol %q is not registered", id)
	}
	if route.Compactor == nil {
		return nil, fmt.Errorf("protocol %q does not support compaction", id)
	}
	return route.Compactor, nil
}
