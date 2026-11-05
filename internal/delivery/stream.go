package delivery

import "context"

// StreamEventType describes a message's generation and delivery lifecycle.
type StreamEventType string

const (
	StreamTextDelta      StreamEventType = "text_delta"
	StreamReasoningDelta StreamEventType = "reasoning_delta"
	StreamReasoningDone  StreamEventType = "reasoning_done"
	StreamReset          StreamEventType = "reset"
	StreamFinish         StreamEventType = "finish"
	StreamSend           StreamEventType = "send"
)

type StreamEvent struct {
	Type    StreamEventType `json:"type"`              // Operation on this logical message.
	Text    string          `json:"text,omitempty"`    // Text/reasoning delta, or the final assistant text.
	Actions []Action        `json:"actions,omitempty"` // Actions accompanying final assistant text.
	Outputs []Output        `json:"outputs,omitempty"` // Independent outputs, exclusive with Text/Actions.
}

// MessageStream belongs to one logical message. Reset permits new generation;
// Finish ends generation but preserves the preview until Send submits the message.
// Only Send produces receipts, including partial receipts when delivery fails.
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
