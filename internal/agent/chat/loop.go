package chat

import (
	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/tool"

	"context"
	"fmt"
	"time"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type Loop struct {
	Identity interface {
		Scope(context.Context) session.Scope
	}
	SelectionForTurn           func(context.Context, *storage.Session) modelmgr.Selection
	NewToolTranscriptCommitter func() agentshare.ToolTranscriptCommitter

	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	compactor contextmgr.Compactor
	Contexts  interface {
		ConsumeSeed(context.Context, string) (*storage.Session, error)
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
		PrepareMaterials(context.Context, []llm.LLMMessage) ([]llm.LLMMessage, media.UsageID, error)
		FinishMaterials(context.Context, media.UsageID)
	}
	Tools interface {
		Schemas(context.Context, *storage.Session) ([]llm.ToolSchema, error)
		MaxRoundsPerTurn() int
		ExecuteWithCommitter(context.Context, *storage.Session, []llm.ToolCallRequest, string, string, agentshare.Output, tool.ToolCommitter) tool.RunResult
	}
	Messages interface {
		Commit(context.Context, storage.DialogueCommit, string) (events.LogResult, error)
		Append(context.Context, *storage.Message, string) (events.LogResult, error)
		Committer(string) agentshare.MessageCommitter
	}
	Caller        *Caller
	PromptBuilder PromptBuilder
}
type preparedLoop struct {
	route                    *Loop
	materials                agentshare.TurnMaterials
	state                    *chatTurnState
	compactSeedOnCurrentUser bool
	summaryOnCurrentUser     bool
}

// CheckSelection validates the native capability before any turn input is saved.
func (r *Loop) CheckSelection(selection modelmgr.Selection) error {
	if _, ok := selection.Client.(chatcompletions.Streamer); !ok {
		return fmt.Errorf("provider %q does not provide the Chat streaming capability", selection.Provider)
	}
	return nil
}

type chatTurnState struct {
	ctx        context.Context
	requestCtx context.Context
	session    *storage.Session
	text       string
	output     agentshare.Output
	selection  modelmgr.Selection
	requestID  string
	startedAt  time.Time
	messages   []llm.LLMMessage
	tools      []llm.ToolSchema
	usage      *llm.Usage
}

// LoopDeps contains protocol collaborators, never another protocol implementation.
type LoopDeps struct {
	Identity interface {
		Scope(context.Context) session.Scope
	}
	SelectionForTurn           func(context.Context, *storage.Session) modelmgr.Selection
	NewToolTranscriptCommitter func() agentshare.ToolTranscriptCommitter

	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	ToolCalls interface {
		SuccessfulIDs(context.Context, []string) (map[string]bool, error)
	}
	Loader interface {
		LoadRawMessages(context.Context, string) ([]storage.Message, error)
	}
	Contexts interface {
		Load(context.Context, string) (*contextmgr.LoadedContext, error)
		ConsumeSeed(context.Context, string) (*storage.Session, error)
	}
	Models interface {
		ResolveCompact(modelmgr.Selection) modelmgr.Selection
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
		PrepareMaterials(context.Context, []llm.LLMMessage) ([]llm.LLMMessage, media.UsageID, error)
		FinishMaterials(context.Context, media.UsageID)
	}
	Tools interface {
		Schemas(context.Context, *storage.Session) ([]llm.ToolSchema, error)
		MaxRoundsPerTurn() int
		ExecuteWithCommitter(context.Context, *storage.Session, []llm.ToolCallRequest, string, string, agentshare.Output, tool.ToolCommitter) tool.RunResult
	}
	Messages interface {
		Commit(context.Context, storage.DialogueCommit, string) (events.LogResult, error)
		Append(context.Context, *storage.Message, string) (events.LogResult, error)
		Committer(string) agentshare.MessageCommitter
	}
	Calls interface {
		Begin(string, modelmgr.Selection) agentshare.CallObservation
		PrepareCall(context.Context, *storage.Session, modelmgr.Selection, []llm.LLMMessage, []llm.ToolSchema, *agentshare.PendingUserMessage, agentshare.MessageCommitter, *events.LogRecord) (agentshare.CallInput, error)
		NotifyError(context.Context, string, agentshare.LLMResponse, error)
		CompleteCall(context.Context, *storage.Session, modelmgr.Selection, string, *llm.Usage, []llm.ToolCallRequest, int64) (agentshare.CallOutput, error)
		RecordVisionFallback(context.Context, *storage.Session)
	}
	System interface {
		Build(context.Context, agentshare.SystemPromptRequest) (string, error)
	}
}

func NewLoop(deps LoopDeps) *Loop {
	return &Loop{
		Identity: deps.Identity, SelectionForTurn: deps.SelectionForTurn,
		NewToolTranscriptCommitter: deps.NewToolTranscriptCommitter,
		Status:                     deps.Status,
		Contexts:                   deps.Contexts,
		Turns:                      deps.Turns,
		View:                       deps.View,
		Preparer:                   deps.Preparer,
		Tools:                      deps.Tools,
		Messages:                   deps.Messages,
		Caller:                     &Caller{Calls: deps.Calls, Messages: deps.Messages, Preparer: deps.Preparer},
		PromptBuilder:              PromptBuilder{System: deps.System},
		compactor:                  &Compactor{ToolCalls: deps.ToolCalls, Models: deps.Models, Contexts: deps.Contexts, Loader: deps.Loader},
	}
}

func (r *Loop) Compactor() contextmgr.Compactor    { return r.compactor }
func (r *Loop) Material() session.MaterialPreparer { return session.DisplayMaterial{} }
