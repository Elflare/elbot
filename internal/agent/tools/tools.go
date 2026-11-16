package tools

import (
	agentshare "elbot/internal/agent/share"

	"context"

	"elbot/internal/fileops"
)

type ToolRuntimeState struct {
	provider     agentshare.ToolNameProvider
	fileRollback interface {
		WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
	}
}

// ToolRuntimeStateDeps declares only the capabilities consumed by ToolRuntimeState.
type ToolRuntimeStateDeps struct {
	FileRollback interface {
		WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
	}
}

// NewToolRuntimeState initializes ToolRuntimeState without installing cross-service callbacks.
func NewToolRuntimeState(deps ToolRuntimeStateDeps) *ToolRuntimeState {
	runtime := &ToolRuntimeState{
		fileRollback: deps.FileRollback,
	}
	runtime.provider = toolRunPromptProvider{}
	return runtime
}

func (r *ToolRuntimeState) Provider() agentshare.ToolNameProvider { return r.provider }
