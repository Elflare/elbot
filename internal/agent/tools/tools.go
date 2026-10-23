package tools

import (
	"elbot/internal/agent/dialogue"
	"elbot/internal/fileops"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

type ToolRuntimeState struct {
	provider        dialogue.ToolSchemaProvider
	manager         *toolrun.Manager
	registry        *tool.Registry
	fileRollback    *fileops.Service
	defaultProvider bool
}

// ToolRuntimeStateDeps declares only the capabilities consumed by ToolRuntimeState.
type ToolRuntimeStateDeps struct {
	Identity     dialogue.Identity
	Provider     dialogue.ToolSchemaProvider
	Manager      *toolrun.Manager
	Registry     *tool.Registry
	FileRollback *fileops.Service
}

// NewToolRuntimeState initializes ToolRuntimeState without installing cross-service callbacks.
func NewToolRuntimeState(deps ToolRuntimeStateDeps) *ToolRuntimeState {
	runtime := &ToolRuntimeState{
		manager:      deps.Manager,
		registry:     deps.Registry,
		fileRollback: deps.FileRollback,
	}
	runtime.provider = toolRunPromptProvider{tools: deps.Manager, identity: deps.Identity}
	runtime.defaultProvider = true
	if deps.Provider != nil {
		runtime.provider = deps.Provider
		runtime.defaultProvider = false
	}
	return runtime
}

func (r *ToolRuntimeState) Provider() dialogue.ToolSchemaProvider { return r.provider }

func (r *ToolRuntimeState) DefaultProvider() bool { return r.defaultProvider }
