package agent

import (
	"context"
	"log/slog"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type ExecutionDialogue interface {
	CheckSelection(modelmgr.Selection) error
	PrepareTurn(context.Context, dialogue.TurnInput) (*dialogue.PreparedTurn, error)
	RunTurn(context.Context, context.Context, dialogue.TurnInput, dialogue.Output) dialogue.TurnResult
}

type ExecutionSender interface {
	SendChat(context.Context, string) error
	StartStream(context.Context) (delivery.MessageStream, error)
	FinishIntermediate(context.Context, context.Context, delivery.MessageStream, string, bool) error
	ReplaceAndFinishStream(context.Context, context.Context, delivery.MessageStream, string) (delivery.Receipt, events.LogResult, error)
	SendAssistant(context.Context, string) (delivery.Receipt, events.LogResult, error)
	SendOutputs(context.Context, []delivery.Output) (events.LogResult, error)
	Notify(context.Context, slog.Level, string)
	// Preview takes one complete notice per tool call; multiline bodies stay in
	// one argument. General workflow notices also occupy one argument.
	Preview(context.Context, ...string)
	StartReasoning(context.Context) dialogue.ReasoningStream
}

type ExecutionStatus interface {
	Record(context.Context, runtimestatus.Snapshot)
	Snapshot(string) runtimestatus.Snapshot
}

type ConfirmationWaitPolicy interface {
	WaitTimeout(context.Context) time.Duration
}

// ExecutionCoordinatorDeps declares only the capabilities consumed by ExecutionCoordinator.
type ExecutionCoordinatorDeps struct {
	Sessions    *session.Service
	SessionRows storage.SessionRepository
	Turns       *turn.Manager
	Requests    *request.Manager
	Contexts    *contextmgr.Service
	Models      *modelmgr.Service
	Dialogue    ExecutionDialogue
	Identity    dialogue.Identity
	View        dialogue.ExecutionView
	Output      ExecutionSender
	Status      ExecutionStatus
	WaitPolicy  ConfirmationWaitPolicy
	// ResponseTimeout bounds one response; non-positive values disable the deadline.
	ResponseTimeout time.Duration
}
