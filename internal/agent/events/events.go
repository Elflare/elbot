package events

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/signal"
)

type ModelCallCompletedEvent struct {
	Execution                                  contextinfo.Execution // Identity of the execution that produced this call result.
	Provider, Model, Text, SourceText          string
	ToolCallCount                              int
	ElapsedMS                                  int64
	Usage                                      *llm.Usage
	Err                                        error
	OutputReady, ProviderError, VisionFallback bool
}

type StatusChangedEvent struct {
	Snapshot     runtimestatus.Snapshot    // Committed execution state, independent of presentation.
	Version      uint64                    // Monotonic publication order within the recorder, not a binding version.
	Execution    contextinfo.Execution     // Identity of the execution that published the state, including terminal states.
	Conversation *contextinfo.Conversation // Original presentation destination; nil when no conversation exists.
	Binding      *BindingSnapshot          // Activation associated with this state; nil when unbound.
}

// BindingSnapshot describes an activation without exposing its internal handle.
type BindingSnapshot struct {
	Scope     session.Scope // Conversation and actor scope of the activation.
	SessionID string        // Persisted session selected by this activation.
	Version   uint64        // Service-local version assigned when this activation was established.
}

// BindingChangedEvent contains independently copied facts for one subscriber.
// At least one of Old and New is present.
type BindingChangedEvent struct {
	Version uint64           // Service-local committed change version; unbinding also advances it.
	Old     *BindingSnapshot // Previous activation; nil when none existed.
	New     *BindingSnapshot // New activation; nil when the scope becomes unbound.
}

type VisionFallbackUsedEvent struct {
	SessionID    string                    // Session used to deduplicate fallback notices.
	Visible      bool                      // Whether this fallback should be shown to the user.
	Conversation *contextinfo.Conversation // Original notification destination; nil when no conversation exists.
}

type Signals struct {
	ModelCallCompleted *signal.Signal[ModelCallCompletedEvent, signal.Empty]
	StatusChanged      *signal.Signal[StatusChangedEvent, signal.Empty]
	VisionFallbackUsed *signal.Signal[VisionFallbackUsedEvent, signal.Empty]
}

func NewSignals() Signals {
	return Signals{
		ModelCallCompleted: signal.New[ModelCallCompletedEvent, signal.Empty]("agent.model_completed"),
		StatusChanged:      signal.New[StatusChangedEvent, signal.Empty]("agent.status_changed"),
		VisionFallbackUsed: signal.New[VisionFallbackUsedEvent, signal.Empty]("agent.vision_fallback"),
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
