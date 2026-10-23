package tools

import (
	"context"

	"elbot/internal/agent/dialogue"
	"elbot/internal/fileops"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

type ToolRuntimeState struct {
	provider     dialogue.ToolNameProvider
	fileRollback interface {
		WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
	}
}

// ToolRuntimeStateDeps declares only the capabilities consumed by ToolRuntimeState.
type ToolRuntimeStateDeps struct {
	Identity dialogue.Identity
	Manager  interface {
		ToolInfos(context.Context, toolrun.Context) ([]tool.Info, error)
	}
	FileRollback interface {
		WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
	}
}

// NewToolRuntimeState initializes ToolRuntimeState without installing cross-service callbacks.
func NewToolRuntimeState(deps ToolRuntimeStateDeps) *ToolRuntimeState {
	runtime := &ToolRuntimeState{
		fileRollback: deps.FileRollback,
	}
	runtime.provider = toolRunPromptProvider{tools: deps.Manager, identity: deps.Identity}
	return runtime
}

func (r *ToolRuntimeState) Provider() dialogue.ToolNameProvider { return r.provider }
