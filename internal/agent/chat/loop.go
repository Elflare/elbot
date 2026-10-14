package chat

import (
	"context"
	"fmt"
	"time"

	"elbot/internal/agent/dialogue"
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
	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	compactor     contextmgr.Compactor
	Contexts      *contextmgr.Service
	Models        *modelmgr.Service
	Turns         *turn.Manager
	View          dialogue.ExecutionView
	Preparer      *dialogue.Preparer
	Tools         *dialogue.ToolExecutor
	Messages      *dialogue.MessageStore
	Caller        *Caller
	PromptBuilder PromptBuilder
}
type preparedLoop struct {
	route                    *Loop
	materials                dialogue.TurnMaterials
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
	output     dialogue.Output
	selection  modelmgr.Selection
	requestID  string
	startedAt  time.Time
	messages   []llm.LLMMessage
	tools      []llm.ToolSchema
	usage      *llm.Usage
}

// LoopDeps contains protocol collaborators, never another protocol implementation.
type LoopDeps struct {
	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	Store    storage.Store
	Contexts *contextmgr.Service
	Models   *modelmgr.Service
	Turns    *turn.Manager
	View     dialogue.ExecutionView
	Preparer *dialogue.Preparer
	Tools    *dialogue.ToolExecutor
	Messages *dialogue.MessageStore
	Calls    *dialogue.CallProcessor
	System   dialogue.SystemPromptManager
}

func NewLoop(deps LoopDeps) *Loop {
	return &Loop{
		Status:        deps.Status,
		Contexts:      deps.Contexts,
		Models:        deps.Models,
		Turns:         deps.Turns,
		View:          deps.View,
		Preparer:      deps.Preparer,
		Tools:         deps.Tools,
		Messages:      deps.Messages,
		Caller:        &Caller{Calls: deps.Calls},
		PromptBuilder: PromptBuilder{System: deps.System},
		compactor:     &Compactor{Store: deps.Store, Models: deps.Models, Contexts: deps.Contexts, Loader: contextmgr.Loader{Store: deps.Store}},
	}
}

func (r *Loop) Compactor() contextmgr.Compactor    { return r.compactor }
func (r *Loop) Material() session.MaterialPreparer { return session.DisplayMaterial{} }
