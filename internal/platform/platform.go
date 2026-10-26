// platform.go 保留旧平台契约、消息上下文与通知辅助定义。
// TODO：按职责迁入contracts.go 与 share 对应文件并更新调用方后删除本文件。

package platform

import (
	"context"
	"encoding/json"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
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

// Runtime is the lifecycle and send surface shared by platform adapters.
type Runtime interface {
	Name() string
	Run(ctx context.Context, handler PlatformHandler) error
	delivery.MessageSender
}

// NotificationReceiver only connects a sender; scheduling belongs to the signal.
type NotificationReceiver interface {
	ConnectNotifications(*signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error)
}

// NotificationTarget retains native reply routing when the destination matches
// the captured origin. Explicit destinations use the existing platform protocol.
func NotificationTarget(n events.Notification) delivery.Target {
	if n.Origin != nil {
		origin := n.Origin.Source
		kind, id := origin.ConversationKind, origin.ConversationID
		if k, v, ok := strings.Cut(origin.ScopeID, ":"); ok {
			if kind == "" || kind == contextinfo.ConversationUnknown {
				kind = contextinfo.ConversationKind(k)
			}
			if id == "" {
				id = v
			}
		}
		if n.Target.Platform == origin.Platform && n.Target.ID == id && n.Target.Kind == kind && (n.Target.ScopeID == "" || n.Target.ScopeID == origin.ScopeID) {
			return delivery.Target{}
		}
	}
	t := delivery.Target{Platform: n.Target.Platform, ScopeID: n.Target.ScopeID}
	if n.Target.Kind == contextinfo.ConversationPrivate {
		t.PrivateUserID = n.Target.ID
	} else {
		t.GroupID = n.Target.ID
	}
	return t
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
	Type           MessageSegmentType `json:"type"`
	Text           string             `json:"text,omitempty"`
	UserID         string             `json:"user_id,omitempty"`
	URL            string             `json:"url,omitempty"`
	MediaID        string             `json:"media,omitempty"`
	PlatformFileID string             `json:"platform_file_id,omitempty"`
	MIMEType       string             `json:"mime_type,omitempty"`
	Name           string             `json:"name,omitempty"`
	Size           int64              `json:"size,omitempty"`
}

type MediaResolver interface {
	ResolveMedia(context.Context, MessageSegment, int64) (delivery.Source, error)
}

type ReplyContext struct {
	MessageID  string
	SenderID   string
	SenderName string
	Text       string
	Segments   []MessageSegment
}

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
	contextinfo.Conversation
	GroupRole             contextinfo.GroupRole
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
	PlatformMessage       json.RawMessage
	Bot                   Identity
	Mentions              []Mention
	TriggerKeywords       []string
	MediaResolver         MediaResolver
}

type messageContextKey struct{}

func WithMessageContext(ctx context.Context, msg MessageContext) context.Context {
	ctx = contextinfo.WithConversation(ctx, msg.Conversation)
	// Only contextinfo owns the public snapshot. The platform value contains
	// private state; readers receive a projection of the current public facts.
	msg.Conversation = contextinfo.Conversation{}
	return context.WithValue(ctx, messageContextKey{}, msg)
}

func MessageContextFrom(ctx context.Context) (MessageContext, bool) {
	msg, ok := ctx.Value(messageContextKey{}).(MessageContext)
	if ok {
		msg.Conversation, _ = contextinfo.ConversationFromContext(ctx)
	}
	return msg, ok
}

func WithoutMessageContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, messageContextKey{}, struct{}{})
}
