package routes

import (
	agentshare "elbot/internal/agent/share"

	"fmt"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/session"
)

// Registry contains protocol capabilities only. Provider reloads do not change it.
type Registry struct {
	protocols map[llm.APIType]Binding
}

func New() *Registry { return &Registry{protocols: make(map[llm.APIType]Binding)} }

func (r *Registry) Register(binding Binding) {
	r.protocols[binding.APIType] = binding
}

func (r *Registry) binding(apiType llm.APIType) (Binding, error) {
	binding, ok := r.protocols[apiType]
	if !ok {
		return Binding{}, fmt.Errorf("protocol %q is not registered", apiType)
	}
	return binding, nil
}

func (r *Registry) LoopFor(apiType llm.APIType) (agentshare.Loop, error) {
	binding, err := r.binding(apiType)
	if err != nil {
		return nil, err
	}
	return binding.Loop, nil
}

func (r *Registry) CompactorFor(origin llm.Origin) (contextmgr.Compactor, error) {
	binding, err := r.binding(origin.APIType)
	if err != nil {
		return nil, err
	}
	return binding.Compactor, nil
}

func (r *Registry) MaterialFor(origin llm.Origin) (session.MaterialPreparer, error) {
	binding, err := r.binding(origin.APIType)
	if err != nil {
		return nil, err
	}
	return binding.Material, nil
}
