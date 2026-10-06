package llm

import (
	"context"
	"time"
)

// Client exposes independent operations, not a particular conversation protocol.
type Client interface {
	ListModels(context.Context) ([]string, error)
	GenerateText(context.Context, TextRequest) (TextResult, error)
}

type TextRequest struct {
	Model           string
	Instructions    string
	Input           string
	MaxOutputTokens int
	ExtraBody       map[string]any
}

type TextResult struct {
	Text  string
	Usage *Usage
}

// Usage contains token usage statistics.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CacheHitTokens   int
}

type ModelMetadata struct {
	ID            string
	ContextWindow int
}

type ModelMetadataProvider interface {
	ListModelMetadata(ctx context.Context) ([]ModelMetadata, error)
}

type RetryEvent struct {
	Attempt    int
	MaxRetries int
	Delay      time.Duration
	Err        error
}

type RetryNotifier interface {
	SetRetryNotifier(func(context.Context, RetryEvent))
}
