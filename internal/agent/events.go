package agent

import (
	"context"
	"fmt"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// EventMeta identifies the published fact, never a mutable execution handle.
type EventMeta struct {
	At                        time.Time
	SessionID, RunID, Attempt string
}

func eventMeta(ctx context.Context, sessionID string) EventMeta {
	m := EventMeta{At: time.Now(), SessionID: sessionID, Attempt: turn.AttemptFromContext(ctx)}
	if e := turn.ExecutionFromContext(ctx); e != nil {
		m.RunID = e.ID
	}
	return m
}

type ModelCallCompletedEvent struct {
	EventMeta
	Provider, Model, Text, SourceText          string
	ToolCallCount                              int
	ElapsedMS                                  int64
	Usage                                      *llm.Usage
	Err                                        error
	OutputReady, ProviderError, VisionFallback bool
}

type UserInputReceivedEvent struct {
	EventMeta
	Text string
}
type PersistenceFailedEvent struct {
	EventMeta
	Operation string
	Err       error
}
type TurnTimedOutEvent struct {
	EventMeta
	Err error
}
type ToolCallCompletedEvent struct {
	EventMeta
	Record    storage.ToolCallRecord
	Arguments string
	RecordErr error
}
type ConfirmationChangedEvent struct {
	EventMeta
	Phase, Action, Tool, Risk, Arguments, Reasons, Extra, Reason, Kind, SandboxDir string
}
type ToolDeniedEvent struct {
	EventMeta
	ActorID, Tool, Risk, Reason string
}
type StatusChangedEvent struct {
	Snapshot runtimestatus.Snapshot
	Version  uint64
	Display  bool
}
type VisionFallbackUsedEvent struct {
	EventMeta
	Visible bool
}
type HookFailedEvent struct {
	EventMeta
	Point       hook.Point
	Platform    hook.PlatformContext
	Err         error
	Log, Notice bool
}
type ReplyDeliveredEvent struct {
	EventMeta
	Receipt   delivery.Receipt
	Err       error
	Operation string
	Buffered  bool
}
type AssociationFailure struct {
	Platform, ScopeID string
	PlatformMessageID string
	Err               error
}

func (e AssociationFailure) Error() string {
	return fmt.Sprintf("map platform message %s/%s/%s: %v", e.Platform, e.ScopeID, e.PlatformMessageID, e.Err)
}
func (e AssociationFailure) Unwrap() error { return e.Err }

type ReplyCommittedEvent struct {
	EventMeta
	MessageID         string
	Persisted         bool
	PersistErr        error
	Err               error
	AssociationErrors []error
	Receipt           delivery.Receipt
}

type Signals struct {
	UserInputReceived   *signal.Signal[UserInputReceivedEvent]
	PersistenceFailed   *signal.Signal[PersistenceFailedEvent]
	TurnTimedOut        *signal.Signal[TurnTimedOutEvent]
	ModelCallCompleted  *signal.Signal[ModelCallCompletedEvent]
	ToolCallCompleted   *signal.Signal[ToolCallCompletedEvent]
	ConfirmationChanged *signal.Signal[ConfirmationChangedEvent]
	ToolDenied          *signal.Signal[ToolDeniedEvent]
	StatusChanged       *signal.Signal[StatusChangedEvent]
	VisionFallbackUsed  *signal.Signal[VisionFallbackUsedEvent]
	HookFailed          *signal.Signal[HookFailedEvent]
	ReplyDelivered      *signal.Signal[ReplyDeliveredEvent]
	ReplyCommitted      *signal.Signal[ReplyCommittedEvent]
}

func newSignals() Signals {
	return Signals{
		UserInputReceived:   signal.New[UserInputReceivedEvent]("agent.user_input", nil),
		PersistenceFailed:   signal.New[PersistenceFailedEvent]("agent.persistence_failed", nil),
		TurnTimedOut:        signal.New[TurnTimedOutEvent]("agent.turn_timed_out", nil),
		ModelCallCompleted:  signal.New[ModelCallCompletedEvent]("agent.model_completed", nil),
		ToolCallCompleted:   signal.New[ToolCallCompletedEvent]("agent.tool_completed", nil),
		ConfirmationChanged: signal.New[ConfirmationChangedEvent]("agent.confirmation_changed", nil),
		ToolDenied:          signal.New[ToolDeniedEvent]("agent.tool_denied", nil),
		StatusChanged:       signal.New[StatusChangedEvent]("agent.status_changed", nil),
		VisionFallbackUsed:  signal.New[VisionFallbackUsedEvent]("agent.vision_fallback", nil),
		HookFailed:          signal.New[HookFailedEvent]("agent.hook_failed", nil),
		ReplyDelivered:      signal.New[ReplyDeliveredEvent]("agent.reply_delivered", nil),
		ReplyCommitted:      signal.New[ReplyCommittedEvent]("agent.reply_committed", nil),
	}
}
func (a *Agent) Signals() Signals { return a.signals }
func emitFact[T any](ctx context.Context, source *signal.Signal[T], event T) {
	if source != nil {
		_ = source.Emit(ctx, event)
	}
}
func cloneUsage(usage *llm.Usage) *llm.Usage {
	if usage == nil {
		return nil
	}
	v := *usage
	return &v
}
func cloneReceipt(receipt delivery.Receipt) delivery.Receipt {
	receipt.PlatformMessageIDs = append([]string(nil), receipt.PlatformMessageIDs...)
	receipt.SentMessages = append([]delivery.SentMessage(nil), receipt.SentMessages...)
	for i := range receipt.SentMessages {
		receipt.SentMessages[i].OutputIndexes = append([]int(nil), receipt.SentMessages[i].OutputIndexes...)
	}
	return receipt
}
