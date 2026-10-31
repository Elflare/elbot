package llm

import (
	"context"
	"time"

	"elbot/internal/events"
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

// Usage contains upstream token counts. Protocol sources preserve unknown values;
// consumers must not replace missing counts with zero or derive a missing total.
// Published usage and its pointers are read-only snapshots.
type Usage struct {
	// PromptTokens is the input count reported by the protocol source; nil means
	// missing or invalid, while a non-nil zero is an explicitly reported zero.
	PromptTokens *int64
	// CompletionTokens is the reported output count; nil means missing or invalid.
	// The protocol source supplies only nonnegative counts, including explicit zero.
	CompletionTokens *int64
	// TotalTokens is the reported total, never a sum inferred by the consumer;
	// the protocol source leaves it nil when missing or invalid.
	TotalTokens *int64
	// CacheHitTokens is the reported cached input count; the protocol source leaves
	// it nil when missing or invalid, and preserves an explicitly reported zero.
	CacheHitTokens *int64
}

// CacheHitPercent returns cached/input*100 without rounding. Nil means either
// count is unknown/invalid or input is zero; aggregate overflow is invalid too.
func (u *Usage) CacheHitPercent() *float64 {
	if u == nil || u.PromptTokens == nil || u.CacheHitTokens == nil || *u.PromptTokens <= 0 || *u.CacheHitTokens < 0 {
		return nil
	}
	percent := 100 * float64(*u.CacheHitTokens) / float64(*u.PromptTokens)
	return &percent
}

type ModelMetadata struct {
	ID            string
	ContextWindow int
}

type ModelMetadataProvider interface {
	ListModelMetadata(ctx context.Context) ([]ModelMetadata, error)
}

type RetryEvent struct {
	// Attempt is the next retry number, supplied by the HTTP attempt source.
	Attempt int
	// MaxRetries is the configured retry limit for this call.
	MaxRetries int
	// Delay is the planned wait before retrying; cancellation may interrupt it.
	Delay time.Duration
	// Err is the original error from the completed attempt and is read-only after publication.
	Err error
	// ResultStatus describes the completed attempt, not the eventual model call.
	// The transport source declares it; zero means unspecified, never success.
	ResultStatus events.ResultStatus
}

type RetryNotifier interface {
	SetRetryNotifier(func(context.Context, RetryEvent))
}
