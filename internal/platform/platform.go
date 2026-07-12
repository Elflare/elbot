package platform

import (
	"context"

	"elbot/internal/delivery"
	"elbot/internal/security"
)

// PlatformAdapter is the interface for message platform adapters (CLI, QQ, etc.).
type PlatformAdapter interface {
	Name() string
	Run(ctx context.Context, handler PlatformHandler) error
	delivery.MessageSender
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
	delivery.MessageSender
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
	Size     int64
}

type ReplyContext struct {
	MessageID string
	SenderID  string
	Text      string
	Segments  []MessageSegment
}

type ConversationKind string

const (
	ConversationUnknown ConversationKind = "unknown"
	ConversationPrivate ConversationKind = "private"
	ConversationGroup   ConversationKind = "group"
	ConversationChannel ConversationKind = "channel"
)

type Identity struct {
	UserID   string
	Username string
}

type Mention struct {
	UserID   string
	Username string
	Text     string
}

// MessageContext carries per-message platform routing and actor data.
type MessageContext struct {
	Platform              string
	ActorID               string
	PlatformUserID        string
	Nickname              string
	GroupCard             string
	DisplayName           string
	GroupRole             security.GroupRole
	ScopeID               string
	ConversationKind      ConversationKind
	PlatformMessageID     string
	ReplyToMessageID      string
	ReplyToSenderID       string
	Sender                delivery.ContextSender
	BufferAssistantOutput bool
	ForkFromMessageID     string
	ResumeSessionID       string
	Segments              []MessageSegment
	ContextText           string
	ContextSegments       []MessageSegment
	Reply                 ReplyContext
	Meta                  map[string]any
	RawText               string
	Bot                   Identity
	Mentions              []Mention
	TriggerKeywords       []string
}

type messageContextKey struct{}

func WithMessageContext(ctx context.Context, msg MessageContext) context.Context {
	return context.WithValue(ctx, messageContextKey{}, msg)
}

func MessageContextFrom(ctx context.Context) (MessageContext, bool) {
	msg, ok := ctx.Value(messageContextKey{}).(MessageContext)
	return msg, ok
}
