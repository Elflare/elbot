// Package hooks declares Agent-owned extension points. Providers register one
// callback group; rule execution and protocol adaptation belong to providers.
package hooks

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
)

// Points is one provider's callbacks. Nil callbacks preserve the original value
// or do nothing. Independent Agent requests may call this group concurrently.
// Providers report their execution failures; Agent handles returned business
// errors at the existing call sites without repeating provider logs.
type Points struct {
	// HasRoute is queried before media preparation; CancelRoute handles /cancel.
	HasRoute    func(context.Context, Facts, agentshare.Message) bool
	CancelRoute func(context.Context, Facts, agentshare.Message) bool
	// Includes continuation routing and ordinary rules. Their stop-propagation
	// remains internal to the provider.
	PlatformMessageReceived func(context.Context, Facts, agentshare.Message) (agentshare.PlatformMessageReceivedResult, events.LogResult, error)
	AgentInputPrepared      func(context.Context, Facts, agentshare.Message) ([]llm.MessageSegment, events.LogResult, error)
	LLMTurnPrepared         func(context.Context, Facts, agentshare.LLMInput) (agentshare.LLMInputResult, events.LogResult, error)
	LLMRequestPrepared      func(context.Context, Facts, agentshare.LLMInput) (agentshare.LLMInputResult, events.LogResult, error)
	LLMResponseReceived     func(context.Context, Facts, agentshare.LLMResponse) (agentshare.LLMResponseResult, events.LogResult, error)
	ToolCallPrepared        func(context.Context, Facts, llm.ToolCallRequest) (string, events.LogResult, error)
	ToolCallCompleted       func(context.Context, Facts, agentshare.ToolResult) ([]llm.MessageSegment, events.LogResult, error)
	AgentOutputPrepared     func(context.Context, Facts, string) (string, events.LogResult, error)
	AgentTurnOutputPrepared func(context.Context, Facts, string) (string, events.LogResult, error)
	PlatformMessageSent     func(context.Context, Facts, string)
	ErrorOccurred           func(context.Context, Facts, agentshare.LLMResponse, error)
}

// Facts contains Agent facts, not a Hook protocol envelope. Conversation,
// execution and original platform message facts remain available through their
// owning packages' context accessors. Providers must honor read-only snapshots.
type Facts struct {
	Session       agentshare.Session // agentshare.Session snapshot; empty before binding.
	Actor         contextinfo.Actor  // Source identity without defaults for source-free work.
	Woken         bool               // Entry wakeup result; never recomputed after edits.
	ReadOnlyCalls bool               // Reject changes to returned call IDs, names and arguments.
}
