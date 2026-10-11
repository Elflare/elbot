package responses

import (
	"elbot/internal/agent/dialogue"
	"elbot/internal/modelmgr"
 "elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// LoopDeps contains protocol collaborators, never another protocol implementation.
type LoopDeps struct {
	Repository storage.DialogueRepository
	Media *media.Manager
	Models     *modelmgr.Service
	Turns      *turn.Manager
	View       dialogue.ExecutionView
	Preparer   *dialogue.Preparer
	Tools      *dialogue.ToolExecutor
	Messages   *dialogue.MessageStore
	Calls      *dialogue.CallProcessor
	System     dialogue.SystemPromptManager
}
