package routes

import (
	"fmt"
	"reflect"
	"sync"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
)

type Registry struct {
	mu         sync.RWMutex
	sealed     bool
	providers  map[string]Binding
	compactors map[llm.ProtocolID]contextmgr.Compactor
}

func New() *Registry {
	return &Registry{providers: make(map[string]Binding), compactors: make(map[llm.ProtocolID]contextmgr.Compactor)}
}

func (r *Registry) Register(binding Binding) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("provider bindings are sealed")
	}
	if binding.Origin.Provider == "" || binding.Origin.Protocol == "" || isNil(binding.Client) {
		return fmt.Errorf("provider binding requires an identity and client")
	}
	if _, ok := r.providers[binding.Origin.Provider]; ok {
		return fmt.Errorf("provider %q already registered", binding.Origin.Provider)
	}
	if isNil(binding.Loop) {
		binding.Loop = nil
	}
	if isNil(binding.Compactor) {
		binding.Compactor = nil
	}
	r.providers[binding.Origin.Provider] = binding
	return nil
}

// RegisterCompactor installs the source-material capability independently of
// configured provider aliases. In particular, migrated Chat material remains
// compressible when its old provider has been removed or changed.
func (r *Registry) RegisterCompactor(id llm.ProtocolID, compactor contextmgr.Compactor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("provider bindings are sealed")
	}
	if id == "" || isNil(compactor) {
		return fmt.Errorf("source compactor requires an identity and capability")
	}
	if _, ok := r.compactors[id]; ok {
		return fmt.Errorf("source compactor %q already registered", id)
	}
	r.compactors[id] = compactor
	return nil
}

func (r *Registry) Seal() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("provider bindings are already sealed")
	}
	if len(r.providers) == 0 {
		return fmt.Errorf("no provider bindings registered")
	}
	for provider, binding := range r.providers {
		if binding.Compactor != nil && r.compactors[binding.Origin.Protocol] == nil {
			return fmt.Errorf("provider %q source compactor is not registered", provider)
		}
	}
	r.sealed = true
	return nil
}

func (r *Registry) LoopFor(provider string) (dialogue.Loop, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.sealed {
		return nil, fmt.Errorf("provider bindings are not sealed")
	}
	binding, ok := r.providers[provider]
	if !ok {
		return nil, fmt.Errorf("provider %q is not registered", provider)
	}
	if binding.Loop == nil {
		return nil, fmt.Errorf("provider %q does not support main dialogue", provider)
	}
	return binding.Loop, nil
}

func (r *Registry) OriginFor(provider string) (llm.Origin, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.sealed {
		return llm.Origin{}, fmt.Errorf("provider bindings are not sealed")
	}
	binding, ok := r.providers[provider]
	if !ok {
		return llm.Origin{}, fmt.Errorf("provider %q is not registered", provider)
	}
	return binding.Origin, nil
}

func (r *Registry) CompactorFor(origin llm.Origin) (contextmgr.Compactor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.sealed {
		return nil, fmt.Errorf("provider bindings are not sealed")
	}
	if origin.Protocol == "" {
		return nil, fmt.Errorf("source session origin is missing")
	}
	if binding, ok := r.providers[origin.Provider]; ok && binding.Origin == origin && binding.Compactor != nil {
		return binding.Compactor, nil
	}
	compactor := r.compactors[origin.Protocol]
	if compactor == nil {
		return nil, fmt.Errorf("source protocol %q does not support compaction", origin.Protocol)
	}
	return compactor, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
