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

func NewLoop(deps LoopDeps) (*Loop, error) {
	if deps.Status == nil {
		return nil, fmt.Errorf("Status is required")
	}
	if deps.Store == nil {
		return nil, fmt.Errorf("Store is required")
	}
	if deps.Contexts == nil {
		return nil, fmt.Errorf("Contexts is required")
	}
	if deps.Models == nil {
		return nil, fmt.Errorf("Models is required")
	}
	if deps.Turns == nil {
		return nil, fmt.Errorf("Turns is required")
	}
	if deps.Preparer == nil {
		return nil, fmt.Errorf("Preparer is required")
	}
	if deps.Tools == nil {
		return nil, fmt.Errorf("Tools is required")
	}
	if deps.Messages == nil {
		return nil, fmt.Errorf("Messages is required")
	}
	if deps.Calls == nil {
		return nil, fmt.Errorf("Calls is required")
	}
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
	}, nil
}

func (r *Loop) Compactor() contextmgr.Compactor    { return r.compactor }
func (r *Loop) Material() session.MaterialPreparer { return session.DisplayMaterial{} }
