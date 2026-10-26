package share

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/signal"

	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
)

type ExecutionStatus interface {
	Record(context.Context, runtimestatus.Snapshot)
	Snapshot(string) runtimestatus.Snapshot
	Usage(*storage.Session) (*llm.Usage, error)
}

type ModelCallCompletedEvent struct {
	Execution                                  contextinfo.Execution // Identity of the execution that produced this call result.
	Provider, Model, Text, SourceText          string
	ToolCallCount                              int
	ElapsedMS                                  int64
	Usage                                      *llm.Usage
	Err                                        error
	OutputReady, ProviderError, VisionFallback bool
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
