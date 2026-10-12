package routes

import (
	"fmt"
	"reflect"
	"sync"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/session"
)

// Registry contains protocol capabilities only. Provider reloads do not change it.
type Registry struct {
	mu        sync.RWMutex
	sealed    bool
	protocols map[llm.APIType]Binding
}

func New() *Registry { return &Registry{protocols: make(map[llm.APIType]Binding)} }

func (r *Registry) Register(binding Binding) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("protocol bindings are sealed")
	}
	if binding.APIType == "" {
		return fmt.Errorf("protocol binding requires an API type")
	}
	if _, exists := r.protocols[binding.APIType]; exists {
		return fmt.Errorf("protocol %q already registered", binding.APIType)
	}
	if isNil(binding.Loop) {
		binding.Loop = nil
	}
	if isNil(binding.Compactor) {
		binding.Compactor = nil
	}
	if isNil(binding.Material) {
		binding.Material = nil
	}
	r.protocols[binding.APIType] = binding
	return nil
}

func (r *Registry) Seal() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("protocol bindings are already sealed")
	}
	if len(r.protocols) == 0 {
		return fmt.Errorf("no protocol bindings registered")
	}
	r.sealed = true
	return nil
}

func (r *Registry) binding(apiType llm.APIType) (Binding, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.sealed {
		return Binding{}, fmt.Errorf("protocol bindings are not sealed")
	}
	binding, ok := r.protocols[apiType]
	if !ok {
		return Binding{}, fmt.Errorf("protocol %q is not registered", apiType)
	}
	return binding, nil
}

func (r *Registry) LoopFor(apiType llm.APIType) (dialogue.Loop, error) {
	binding, err := r.binding(apiType)
	if err != nil {
		return nil, err
	}
	if binding.Loop == nil {
		return nil, fmt.Errorf("protocol %q does not support main dialogue", apiType)
	}
	return binding.Loop, nil
}

func (r *Registry) CompactorFor(origin llm.Origin) (contextmgr.Compactor, error) {
	binding, err := r.binding(origin.APIType)
	if err != nil {
		return nil, err
	}
	if binding.Compactor == nil {
		return nil, fmt.Errorf("source protocol %q does not support compaction", origin.APIType)
	}
	return binding.Compactor, nil
}

func (r *Registry) MaterialFor(origin llm.Origin) (session.MaterialPreparer, error) {
	binding, err := r.binding(origin.APIType)
	if err != nil {
		return nil, err
	}
	if binding.Material == nil {
		return nil, fmt.Errorf("source protocol %q does not support material branching", origin.APIType)
	}
	return binding.Material, nil
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
