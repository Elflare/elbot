package chat

import (
	agentshare "elbot/internal/agent/share"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/tool"

	"context"
	"fmt"
	"time"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/turn"
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
	ctx        context.Context
	requestCtx context.Context
	session    *storage.Session
	output     agentshare.Output
	selection  modelmgr.Selection
	requestID  string
	startedAt  time.Time
	messages   []llm.LLMMessage
	tools      []llm.ToolSchema
	usage      *llm.Usage
}

// LoopDeps contains runtime capabilities and collaborators assembled by the Agent root.
type LoopDeps struct {
	NewMessageID func(context.Context) (string, error) // 根部注入的消息发号能力。
	Identity     interface {
		Scope(context.Context) session.Scope
	}
	SelectionForTurn           func(context.Context, *storage.Session) modelmgr.Selection
	NewToolTranscriptCommitter func() agentshare.ToolTranscriptCommitter

	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	Turns interface {
		StartToolPhase(string, string) bool
		DrainMergedInput(string, string) turn.Input
	}
	View interface {
		Context(context.Context) context.Context
		RefreshSession(context.Context, *storage.Session) (context.Context, error)
		WithModel(context.Context, modelmgr.Selection) (context.Context, error)
		CheckSelection(*storage.Session, modelmgr.Selection) (operationresult.Result[struct{}], error)
	}
	Preparer interface {
		PrepareInput(context.Context, agentshare.LoopInput, *storage.Message, string, []llm.MessageSegment, []llm.LLMMessage, []llm.ToolSchema, bool) ([]llm.MessageSegment, []llm.ToolSchema, error)
	}
	Tools interface {
		Schemas(context.Context, *storage.Session) ([]llm.ToolSchema, error)
		MaxRoundsPerTurn() int
		ExecuteWithCommitter(context.Context, *storage.Session, []llm.ToolCallRequest, string, string, string, agentshare.Output, tool.ToolCommitter) tool.RunResult
	}
	Messages interface {
		Commit(context.Context, storage.DialogueCommit, string) error
		Append(context.Context, *storage.Message, string) error
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
