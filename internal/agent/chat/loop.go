package chat

import (
	"context"
	"log/slog"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type Loop struct {
	Logger        *slog.Logger
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
