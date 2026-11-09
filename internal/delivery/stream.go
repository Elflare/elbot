package delivery

import (
	"context"

	"elbot/internal/llm"
)

// StreamEventType describes a message's generation and delivery lifecycle.
type StreamEventType string

const (
	StreamTextDelta      StreamEventType = "text_delta"
	StreamReasoningDelta StreamEventType = "reasoning_delta"
	StreamReasoningDone  StreamEventType = "reasoning_done"
	StreamReset          StreamEventType = "reset"
	StreamFinish         StreamEventType = "finish"
	StreamSend           StreamEventType = "send"
	StreamSendExtras     StreamEventType = "send_extras"
	StreamToolResult     StreamEventType = "tool_result"
)

type ToolResult struct {
	Name     string               `json:"name"`     // Registered tool name.
	Success  bool                 `json:"success"`  // Whether tool execution completed without error.
	Segments []llm.MessageSegment `json:"segments"` // Complete result content, including multimodal parts.
}

type StreamEvent struct {
	Type       StreamEventType `json:"type"`                   // Operation on this logical message.
	Text       string          `json:"text,omitempty"`         // Text/reasoning delta, or the final assistant text.
	Actions    []Action        `json:"actions,omitempty"`      // Actions accompanying final assistant text.
	Outputs    []Output        `json:"outputs,omitempty"`      // Independent content carried only by SendExtras.
	ToolCallID string          `json:"tool_call_id,omitempty"` // Associates a result event with its tool call.
	ToolResult *ToolResult     `json:"tool_result,omitempty"`  // Present on StreamToolResult.
}

// MessageStream belongs to one logical message. Reset clears its current preview
// and permits new generation on the same stream, including after a failed attempt;
// Finish ends generation but preserves the preview until Send submits the message.
// Send replaces the whole body (even when empty), leaving reasoning unchanged.
// SendExtras delivers independent content at the Agent output boundary.
// Send and SendExtras produce receipts, including partial delivery failures.
type MessageStream interface {
	Send(context.Context, StreamEvent) (Receipt, error)
}

type StreamSender interface {
	StartStream(context.Context) (MessageStream, error)
}

// StreamFunc adapts an event consumer without a separate forwarding object.
type StreamFunc func(context.Context, StreamEvent) (Receipt, error)

func (f StreamFunc) Send(ctx context.Context, event StreamEvent) (Receipt, error) {
	return f(ctx, event)
}
