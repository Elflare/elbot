package responses

import (
	"context"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
)

// LoopDeps contains protocol collaborators, never another protocol implementation.
type LoopDeps struct {
	Shared   agentshare.LoopDeps
	Identity interface {
		Scope(context.Context) session.Scope
	}

	Gate interface {
		Enter(context.Context, string) (context.Context, func(), error)
	}
	SessionRows interface {
		MutateSession(context.Context, string, func(*storage.Session) error) (*storage.Session, error)
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
	Preparer interface {
		PrepareInput(context.Context, agentshare.LoopInput, *storage.Message, string, []llm.MessageSegment, []llm.LLMMessage, []llm.ToolSchema, bool) ([]llm.MessageSegment, []llm.ToolSchema, bool, error)
		ArchiveMaterials(context.Context, []llm.LLMMessage) ([]llm.LLMMessage, error)
		PrepareMaterials(context.Context, []llm.LLMMessage) (operationresult.Result[[]llm.LLMMessage], media.UsageID, error)
		FinishMaterials(context.Context, media.UsageID)
	}
	Messages interface {
		Commit(context.Context, storage.DialogueCommit, string) error
	}
	Calls interface {
		Call(context.Context, *storage.Session, modelmgr.Selection, []llm.LLMMessage, []llm.ToolSchema, *agentshare.PendingInput, agentshare.MessageCommitter, delivery.MessageStream, bool,
			func(context.Context, agentshare.CallInput) (operationresult.Result[struct{}], error),
			func(context.Context, agentshare.CallObservation) (operationresult.Result[agentshare.CallOutput], error)) (operationresult.Result[agentshare.CallOutput], error)
	}
	System interface {
		Build(context.Context, agentshare.SystemPromptRequest) (string, error)
	}
}
