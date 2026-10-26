package responses

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/tool"

	"elbot/internal/modelmgr"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// LoopDeps contains protocol collaborators, never another protocol implementation.
type LoopDeps struct {
	Identity interface {
		Scope(context.Context) session.Scope
	}
	SelectionForTurn func(context.Context, *storage.Session) modelmgr.Selection
	Gate             interface {
		Enter(context.Context, string) (context.Context, func(), error)
	}
	SessionRows interface {
		Mutate(context.Context, string, func(*storage.Session) error) (*storage.Session, error)
	}

	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	Repository interface {
		CreateExchange(context.Context, *storage.NativeExchange) error
		FinishExchange(context.Context, string, string, string, string, string) error
		CurrentCheckpoint(context.Context, string) (*storage.NativeCheckpoint, error)
		CheckpointForMessage(context.Context, string, string) (*storage.NativeCheckpoint, error)
		LoadNativeWindow(context.Context, *storage.Session, *storage.NativeCheckpoint) (*storage.NativeWindowRecords, error)
		PendingInputs(context.Context, string) ([]storage.NativeInput, error)
		Calls(context.Context, string) ([]storage.NativeCall, error)
	}
	Turns interface {
		StartToolPhase(string, ...string) bool
		DrainMergedInput(string, ...string) turn.Input
	}
	View interface {
		Context(context.Context) context.Context
		RefreshSession(context.Context, *storage.Session) (context.Context, error)
		WithModel(context.Context, modelmgr.Selection) (context.Context, error)
		CheckSelection(*storage.Session, modelmgr.Selection) error
	}
	Preparer interface {
		PrepareInput(context.Context, agentshare.LoopInput, *storage.Message, string, []llm.MessageSegment, []llm.LLMMessage, []llm.ToolSchema, bool) ([]llm.MessageSegment, []llm.ToolSchema, error)
		ArchiveMaterials(context.Context, []llm.LLMMessage) ([]llm.LLMMessage, error)
		PrepareMaterials(context.Context, []llm.LLMMessage) ([]llm.LLMMessage, func(), error)
	}
	Tools interface {
		Schemas(context.Context, *storage.Session) ([]llm.ToolSchema, error)
		MaxRoundsPerTurn() int
		ExecuteWithCommitter(context.Context, *storage.Session, []llm.ToolCallRequest, string, string, agentshare.Output, tool.ToolCommitter) tool.RunResult
	}
	Messages interface {
		Commit(context.Context, storage.DialogueCommit, string) (events.LogResult, error)
	}
	Calls interface {
		Begin(string, modelmgr.Selection) agentshare.CallObservation
		PrepareCall(context.Context, *storage.Session, modelmgr.Selection, []llm.LLMMessage, []llm.ToolSchema, *agentshare.PendingUserMessage, agentshare.MessageCommitter, *events.LogRecord) (agentshare.CallInput, error)
		NotifyError(context.Context, string, agentshare.LLMResponse, error)
		CompleteCall(context.Context, *storage.Session, modelmgr.Selection, string, *llm.Usage, []llm.ToolCallRequest, int64) (agentshare.CallOutput, error)
	}
	System interface {
		Build(context.Context, agentshare.SystemPromptRequest) (string, error)
	}
}
