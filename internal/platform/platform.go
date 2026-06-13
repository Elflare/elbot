package platform

import (
	"context"

	"elbot/internal/output"
)

// PlatformAdapter is the interface for message platform adapters (CLI, QQ, etc.).
type PlatformAdapter interface {
	Name() string
	Run(ctx context.Context, handler PlatformHandler) error
	SendChat(ctx context.Context, out output.Output) (Receipt, error)
	SendNotice(ctx context.Context, target output.Target, out output.Output) (Receipt, error)
}

// PlatformHandler processes incoming messages from a platform.
type PlatformHandler interface {
	HandleMessage(ctx context.Context, text string) error
}

// ConnectNotifier is implemented by adapters that can report successful platform connections.
type ConnectNotifier interface {
	SetConnectNotifier(func(context.Context, string))
}

// Runtime is the lifecycle and send surface shared by platform adapters.
type Runtime interface {
	Name() string
	Run(ctx context.Context, handler PlatformHandler) error
	MessageSender
}

// Receipt describes a platform message produced by a send operation.
type Receipt struct {
	PlatformMessageID string
}

// StreamingMessageSender is an optional platform capability for editable streaming output.
// Platforms can implement it with terminal replacement, message editing, or any equivalent mechanism.
type StreamingMessageSender interface {
	StartStream(ctx context.Context) (MessageStream, error)
}

// MessageStream represents one assistant message that can be appended while streaming
// and replaced with the final post-hook content.
type MessageStream interface {
	Append(ctx context.Context, text string) error
	Replace(ctx context.Context, text string) (Receipt, error)
	Finish(ctx context.Context) (Receipt, error)
}

// MessageSender sends chat messages and notifications through a platform.
type MessageSender interface {
	SendChat(ctx context.Context, out output.Output) (Receipt, error)
	SendNotice(ctx context.Context, target output.Target, out output.Output) (Receipt, error)
}

// ContextSender can send a reply using routing information carried by ctx.
type ContextSender interface {
	MessageSender
}

type MessageSegmentType string

const (
	SegmentText  MessageSegmentType = "text"
	SegmentImage MessageSegmentType = "image"
	SegmentFile  MessageSegmentType = "file"
	SegmentAt    MessageSegmentType = "at"
)

// MessageSegment is one typed part parsed from an inbound platform message.
type MessageSegment struct {
	Type     MessageSegmentType
	Text     string
	UserID   string
	URL      string
	MIMEType string
	Name     string
}

// MessageContext carries per-message platform routing and actor data.
type MessageContext struct {
	Platform              string
	ActorID               string
	PlatformUserID        string
	DisplayName           string
	ScopeID               string
	Sender                ContextSender
	BufferAssistantOutput bool
	ForkFromMessageID     string
	Segments              []MessageSegment
	Meta                  map[string]any
}

type messageContextKey struct{}

func WithMessageContext(ctx context.Context, msg MessageContext) context.Context {
	return context.WithValue(ctx, messageContextKey{}, msg)
}

func MessageContextFrom(ctx context.Context) (MessageContext, bool) {
	msg, ok := ctx.Value(messageContextKey{}).(MessageContext)
	return msg, ok
}
