// Package hooks declares Agent-owned extension points. Providers register one
// callback group; rule execution and protocol adaptation belong to providers.
package hooks

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
)

// Points is one provider's callbacks. Nil callbacks preserve the original value
// or do nothing. Independent Agent requests may call this group concurrently.
// Providers report their execution failures; Agent handles returned business
// errors at the existing call sites without repeating provider logs.
type Points struct {
	// HasRoute is queried before media preparation; CancelRoute handles /cancel.
	HasRoute    func(context.Context, Facts, Message) bool
	CancelRoute func(context.Context, Facts, Message) bool
	// Includes continuation routing and ordinary rules. Their stop-propagation
	// remains internal to the provider.
	PlatformMessageReceived func(context.Context, Facts, Message) (PlatformMessageReceivedResult, events.LogResult, error)
	AgentInputPrepared      func(context.Context, Facts, Message) ([]llm.MessageSegment, events.LogResult, error)
	LLMTurnPrepared         func(context.Context, Facts, LLMInput) (LLMInputResult, events.LogResult, error)
	LLMRequestPrepared      func(context.Context, Facts, LLMInput) (LLMInputResult, events.LogResult, error)
	LLMResponseReceived     func(context.Context, Facts, LLMResponse) (LLMResponseResult, events.LogResult, error)
	ToolCallPrepared        func(context.Context, Facts, llm.ToolCallRequest) (string, events.LogResult, error)
	ToolCallCompleted       func(context.Context, Facts, ToolResult) ([]llm.MessageSegment, events.LogResult, error)
	AgentOutputPrepared     func(context.Context, Facts, string) (string, events.LogResult, error)
	AgentTurnOutputPrepared func(context.Context, Facts, string) (string, events.LogResult, error)
	PlatformMessageSent     func(context.Context, Facts, string)
	ErrorOccurred           func(context.Context, Facts, LLMResponse, error)
}

// Facts contains Agent facts, not a Hook protocol envelope. Conversation,
// execution and original platform message facts remain available through their
// owning packages' context accessors. Providers must honor read-only snapshots.
type Facts struct {
	Session       Session           // Session snapshot; empty before binding.
	Actor         contextinfo.Actor // Source identity without defaults for source-free work.
	Woken         bool              // Entry wakeup result; never recomputed after edits.
	ReadOnlyCalls bool              // Reject changes to returned call IDs, names and arguments.
}

type Session struct {
	ID, Mode, Title, Status string // Detached session identity and display facts.
}

type Message struct {
	ID, Role     string               // Persisted identity and role when available.
	PlatformText string               // Original platform text, unaffected by edits.
	IntentText   string               // User text with the entry wakeup prefix removed.
	Segments     []llm.MessageSegment // Editable message bound to this stage.
}

type PlatformMessageReceivedResult struct {
	Segments []llm.MessageSegment // Message after continuation and ordinary rules.
	Consume  bool                 // Stop Agent command/model processing after sending Outputs.
	Outputs  []delivery.Output    // Additional output sent before the consume decision.
}

type LLMInput struct {
	Message         Message          // Initial user input for turn; only new pending for request.
	Provider, Model string           // Selected model facts.
	Messages        []llm.LLMMessage // Detached read-only working context.
	Tools           []llm.ToolSchema // Definitions governed by the existing provider edit policy.
}

type LLMInputResult struct {
	Segments []llm.MessageSegment // Only the message bound in LLMInput may be edited.
	Tools    []llm.ToolSchema     // Returned definitions remain subject to Agent availability.
}

type LLMResponse struct {
	Provider, Model  string                // Selected model facts.
	Text, SourceText string                // Display text and original model text.
	Usage            *llm.Usage            // Model usage snapshot.
	ToolCalls        []llm.ToolCallRequest // Immutable when Facts.ReadOnlyCalls is true.
	ElapsedMS        int64                 // Duration of the completed or failed model call.
}

type LLMResponseResult struct {
	Text, SourceText string                // Display result and original model text.
	Usage            *llm.Usage            // Usage under the existing provider policy.
	ToolCalls        []llm.ToolCallRequest // Calls governed by Facts.ReadOnlyCalls.
	Outputs          []delivery.Output     // Extra outputs retaining their existing delivery timing.
}

type ToolResult struct {
	Call     llm.ToolCallRequest  // Executed call; identity and arguments are facts here.
	Risk     string               // Execution risk classification.
	Segments []llm.MessageSegment // Editable result, including non-text segments.
	Err      error                // Actual execution error, independent of result display.
}

type wakeupKey struct{}
type readOnlyCallsKey struct{}

func WithWakeup(ctx context.Context, woken bool) context.Context {
	return context.WithValue(ctx, wakeupKey{}, woken)
}

// WithReadOnlyCalls records the caller's policy without importing Hook internals.
func WithReadOnlyCalls(ctx context.Context) context.Context {
	return context.WithValue(ctx, readOnlyCallsKey{}, true)
}
