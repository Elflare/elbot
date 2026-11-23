package chat

import (
	"context"
	"fmt"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type Loop struct {
	LoopDeps
	compactor contextmgr.Compactor
}

type preparedLoop struct {
	route                *Loop
	materials            agentshare.TurnMaterials
	state                *chatTurnState
	summaryOnCurrentUser bool
}

// CheckSelection validates the native capability before any turn input is saved.
func (r *Loop) CheckSelection(selection modelmgr.Selection) operationresult.Result[struct{}] {
	if _, ok := selection.Client.(chatcompletions.Streamer); !ok {
		return operationresult.Result[struct{}]{Status: operationresult.Incompatible, Message: fmt.Sprintf("provider %q does not provide the Chat streaming capability", selection.Provider)}
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}
}

type chatTurnState struct {
	agentshare.LoopState
	output   agentshare.Output
	messages []llm.LLMMessage
	tools    []llm.ToolSchema
	usage    *llm.Usage
}

// LoopDeps contains runtime capabilities and collaborators assembled by the Agent root.
type LoopDeps struct {
	Shared   agentshare.LoopDeps
	Identity interface {
		Scope(context.Context) session.Scope
	}

	NewToolTranscriptCommitter func() agentshare.ToolTranscriptCommitter

	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	Preparer interface {
		PrepareInput(context.Context, agentshare.LoopInput, *storage.Message, string, []llm.MessageSegment, []llm.LLMMessage, []llm.ToolSchema, bool) ([]llm.MessageSegment, []llm.ToolSchema, bool, error)
	}
	Messages interface {
		Commit(context.Context, storage.DialogueCommit, string) error
		Committer(string) agentshare.MessageCommitter
	}
	Caller        *Caller
	PromptBuilder PromptBuilder
}

func NewLoop(deps LoopDeps, compactor contextmgr.Compactor) *Loop {
	return &Loop{LoopDeps: deps, compactor: compactor}
}

func (r *Loop) Compactor() contextmgr.Compactor    { return r.compactor }
func (r *Loop) Material() session.MaterialPreparer { return session.DisplayMaterial{} }
