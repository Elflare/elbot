package execution

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"log/slog"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type ExecutionDialogue interface {
	CheckSelection(modelmgr.Selection) error
	PrepareTurn(context.Context, agentshare.TurnInput) (agentshare.PreparedTurn, error)
	RunTurn(context.Context, context.Context, agentshare.TurnInput, agentshare.Output) agentshare.TurnResult
}

type ExecutionSender interface {
	SendChat(context.Context, string) error
	StartStream(context.Context) (delivery.MessageStream, error)
	FinishIntermediate(context.Context, context.Context, delivery.MessageStream, string, bool) error
	ReplaceAndFinishStream(context.Context, context.Context, delivery.MessageStream, string) (delivery.Receipt, events.LogResult, error)
	SendAssistant(context.Context, string, ...delivery.Action) (delivery.Receipt, events.LogResult, error)
	SendOutputs(context.Context, []delivery.Output) (events.LogResult, error)
	Notify(context.Context, slog.Level, string)
	// Preview takes one complete notice per tool call; multiline bodies stay in
	// one argument. General workflow notices also occupy one argument.
	Preview(context.Context, ...string)
	StartReasoning(context.Context) delivery.ReasoningStream
}

// ExecutionCoordinatorDeps declares only the capabilities consumed by ExecutionCoordinator.
type ExecutionCoordinatorDeps struct {
	Sessions interface {
		CreateCompacted(context.Context, session.Scope, string, session.CompactedRequest) (*storage.Session, error)
		Current(context.Context, session.Scope) (*storage.Session, error)
		CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
		EnterActivation(context.Context, session.Scope, ...string) (context.Context, func(), error)
		EnterScope(context.Context, session.Scope) (context.Context, func(), error)
		EnterSessions(context.Context, ...string) (context.Context, func(), error)
		Fork(context.Context, session.Scope, string) (*storage.Session, error)
		GetOrCreateCurrent(context.Context, session.Scope, string) (*storage.Session, error)
		MaybeScheduleNaming(context.Context, string)
		RegisterOrigin(context.Context, string, llm.Origin) (*storage.Session, error)
		Resume(context.Context, session.Scope, string) (*storage.Session, error)
		RecordActivity(context.Context, *session.Binding) error
		SessionIdle(context.Context, string) error
	}
	SessionRows interface {
		Get(context.Context, string) (*storage.Session, error)
	}
	Turns interface {
		AppendPendingInput(string, turn.Input) bool
		BeginAppend(string, string, turn.Input) *turn.AppendWait
		CanCompact(string, *turn.Execution) bool
		CancelAppend(string) bool
		CompleteCompactRun(string, string) bool
		CompleteTurn(string, string) (turn.Input, bool)
		FinishAttempt(string, string)
		ReserveExecution(string, turn.Input, *turn.Execution) bool
		ResumeAppend(string) (turn.Input, *turn.Execution, bool)
		Snapshot(string) turn.Snapshot
		StartCompactRun(string, string, *turn.Execution) bool
		StartAttempt(string, turn.Input, *turn.Execution, string) bool
		StopSession(string)
		StopAttempt(string, string)
	}
	Requests interface {
		CancelSession(string) int
		ListBySession(string) []request.Request
		Start(context.Context, request.StartRequest) (request.Request, context.Context, func())
	}
	Contexts interface {
		Compact(context.Context, *storage.Session, string, modelmgr.Selection) (*contextmgr.PreparedCompact, error)
		ReachedCompactThreshold(context.Context, *llm.Usage, modelmgr.Selection) bool
		RecordTurn(context.Context, string, *llm.Usage) error
	}
	SelectionForTurn func(context.Context, *storage.Session) modelmgr.Selection
	TurnTimeout      string // User-facing notification for a response deadline.
	Dialogue         ExecutionDialogue
	Identity         interface {
		Scope(context.Context) session.Scope
		Actor(context.Context) contextinfo.Actor
	}
	View interface {
		Context(context.Context) context.Context
		WithModel(context.Context, modelmgr.Selection) (context.Context, error)
		CheckSelection(*storage.Session, modelmgr.Selection) error
	}
	Output     ExecutionSender
	Status     agentshare.ExecutionStatus
	WaitPolicy agentshare.ConfirmationWaitPolicy
	// ResponseTimeout bounds one response; non-positive values disable the deadline.
	ResponseTimeout time.Duration
}
