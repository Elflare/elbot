package responses

import (
	"context"

	"elbot/internal/agent/dialogue"
	"elbot/internal/modelmgr"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// LoopDeps contains protocol collaborators, never another protocol implementation.
type LoopDeps struct {
	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	Repository storage.DialogueRepository
	Models     *modelmgr.Service
	Turns      *turn.Manager
	View       dialogue.ExecutionView
	Preparer   *dialogue.Preparer
	Tools      *dialogue.ToolExecutor
	Messages   *dialogue.MessageStore
	Calls      *dialogue.CallProcessor
	System     dialogue.SystemPromptManager
}
