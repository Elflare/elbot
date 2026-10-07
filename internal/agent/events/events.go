package events

import (
	"context"
	"fmt"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

// EventMeta identifies the published fact, never a mutable execution handle.
type EventMeta struct {
	At                        time.Time
	SessionID, RunID, Attempt string
	RequestID, RootRequestID  string
}

func Meta(ctx context.Context, sessionID string) EventMeta {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	return EventMeta{At: time.Now(), SessionID: sessionID, RunID: facts.RunID, Attempt: facts.Attempt,
		RequestID: facts.RequestID, RootRequestID: facts.RootRequestID}
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

func NewSignals() Signals {
	return Signals{
		UserInputReceived:   signal.New[UserInputReceivedEvent]("agent.user_input"),
		PersistenceFailed:   signal.New[PersistenceFailedEvent]("agent.persistence_failed"),
		TurnTimedOut:        signal.New[TurnTimedOutEvent]("agent.turn_timed_out"),
		ModelCallCompleted:  signal.New[ModelCallCompletedEvent]("agent.model_completed"),
		ToolCallCompleted:   signal.New[ToolCallCompletedEvent]("agent.tool_completed"),
		ConfirmationChanged: signal.New[ConfirmationChangedEvent]("agent.confirmation_changed"),
		ToolDenied:          signal.New[ToolDeniedEvent]("agent.tool_denied"),
		StatusChanged:       signal.New[StatusChangedEvent]("agent.status_changed"),
		VisionFallbackUsed:  signal.New[VisionFallbackUsedEvent]("agent.vision_fallback"),
		HookFailed:          signal.New[HookFailedEvent]("agent.hook_failed"),
		ReplyDelivered:      signal.New[ReplyDeliveredEvent]("agent.reply_delivered"),
		ReplyCommitted:      signal.New[ReplyCommittedEvent]("agent.reply_committed"),
	}
}
func Emit[T any](ctx context.Context, source *signal.Signal[T], event T) {
	if source != nil {
		_ = source.Emit(ctx, event)
	}
}
func CloneUsage(usage *llm.Usage) *llm.Usage {
	if usage == nil {
		return nil
	}
	v := *usage
	return &v
}
func CloneReceipt(receipt delivery.Receipt) delivery.Receipt {
	receipt.PlatformMessageIDs = append([]string(nil), receipt.PlatformMessageIDs...)
	receipt.SentMessages = append([]delivery.SentMessage(nil), receipt.SentMessages...)
	for i := range receipt.SentMessages {
		receipt.SentMessages[i].OutputIndexes = append([]int(nil), receipt.SentMessages[i].OutputIndexes...)
	}
	return receipt
}
