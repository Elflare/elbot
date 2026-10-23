package events

import (
	"context"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/hook"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/signal"
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

type StatusChangedEvent struct {
	Snapshot runtimestatus.Snapshot // Committed execution state, independent of presentation.
	Version  uint64                 // Monotonic publication order within the recorder.
}

// BindingSnapshot describes an activation without exposing its internal handle.
type BindingSnapshot struct {
	Scope     session.Scope // Conversation and actor scope of the activation.
	SessionID string        // Persisted session selected by this activation.
}

// BindingChangedEvent contains independently copied facts for one subscriber.
type BindingChangedEvent struct {
	Old    *BindingSnapshot     // Previous activation; nil when none existed.
	New    *BindingSnapshot     // New activation; nil when the scope becomes unbound.
	Reason session.ChangeReason // Operation that changed the selection.
}

type VisionFallbackUsedEvent struct {
	EventMeta
	Visible bool
}
type HookFailedEvent struct {
	EventMeta
	Point    hook.Point
	Platform hook.PlatformContext
	Err      error
	Notice   bool
}
type Signals struct {
	ModelCallCompleted *signal.Signal[ModelCallCompletedEvent, signal.Empty]
	StatusChanged      *signal.Signal[StatusChangedEvent, signal.Empty]
	VisionFallbackUsed *signal.Signal[VisionFallbackUsedEvent, signal.Empty]
	HookFailed         *signal.Signal[HookFailedEvent, signal.Empty]
}

func NewSignals() Signals {
	return Signals{
		ModelCallCompleted: signal.New[ModelCallCompletedEvent, signal.Empty]("agent.model_completed"),
		StatusChanged:      signal.New[StatusChangedEvent, signal.Empty]("agent.status_changed"),
		VisionFallbackUsed: signal.New[VisionFallbackUsedEvent, signal.Empty]("agent.vision_fallback"),
		HookFailed:         signal.New[HookFailedEvent, signal.Empty]("agent.hook_failed"),
	}
}
func Emit[T any](ctx context.Context, source *signal.Signal[T, signal.Empty], event T) {
	if source != nil {
		_, _ = source.Emit(ctx, event)
	}
}
func CloneUsage(usage *llm.Usage) *llm.Usage {
	if usage == nil {
		return nil
	}
	v := *usage
	if usage.PromptTokens != nil {
		v.PromptTokens = new(int64)
		*v.PromptTokens = *usage.PromptTokens
	}
	if usage.CompletionTokens != nil {
		v.CompletionTokens = new(int64)
		*v.CompletionTokens = *usage.CompletionTokens
	}
	if usage.TotalTokens != nil {
		v.TotalTokens = new(int64)
		*v.TotalTokens = *usage.TotalTokens
	}
	if usage.CacheHitTokens != nil {
		v.CacheHitTokens = new(int64)
		*v.CacheHitTokens = *usage.CacheHitTokens
	}
	return &v
}
