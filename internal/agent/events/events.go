package events

import (
	"context"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/hook"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
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
	Point    hook.Point
	Platform hook.PlatformContext
	Err      error
	Notice   bool
}
type Signals struct {
	ModelCallCompleted *signal.Signal[ModelCallCompletedEvent]
	StatusChanged      *signal.Signal[StatusChangedEvent]
	VisionFallbackUsed *signal.Signal[VisionFallbackUsedEvent]
	HookFailed         *signal.Signal[HookFailedEvent]
}

func NewSignals() Signals {
	return Signals{
		ModelCallCompleted: signal.New[ModelCallCompletedEvent]("agent.model_completed"),
		StatusChanged:      signal.New[StatusChangedEvent]("agent.status_changed"),
		VisionFallbackUsed: signal.New[VisionFallbackUsedEvent]("agent.vision_fallback"),
		HookFailed:         signal.New[HookFailedEvent]("agent.hook_failed"),
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
