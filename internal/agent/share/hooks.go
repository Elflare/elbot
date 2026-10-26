package share

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/llm"
)

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

// WakeupFromContext preserves the default for entries without a wakeup decision.
func WakeupFromContext(ctx context.Context) bool {
	woken, ok := ctx.Value(wakeupKey{}).(bool)
	return !ok || woken
}

// ReadOnlyCallsFromContext reports whether call identities and arguments are immutable.
func ReadOnlyCallsFromContext(ctx context.Context) bool {
	readonly, _ := ctx.Value(readOnlyCallsKey{}).(bool)
	return readonly
}
