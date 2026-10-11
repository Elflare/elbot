package events

import (
	"context"
	"log/slog"

	"elbot/internal/contextinfo"
	"elbot/internal/signal"
)

// Notification carries plain-text runtime hints and command results, never chat or media jobs.
type Notification struct {
	Target NotificationTarget        // Explicit destination; an empty target uses Origin.
	Text   string                    // Plain text, independent of platform segments or media preparation.
	Level  slog.Level                // Presentation/logging severity, independent of send success.
	Origin *contextinfo.Conversation // Immutable original reply/connection facts.
}
type NotificationTarget struct {
	Platform string                       // Runtime platform name, e.g. cli or telegram.
	Kind     contextinfo.ConversationKind // Private, group or channel destination.
	ID       string                       // Platform user, group or channel ID, according to Kind.
	ScopeID  string                       // Platform-specific scope needed to address the conversation.
}
type NotificationReceipt struct {
	PlatformMessageIDs []string // Actually sent messages; may be partial on failure.
}

// Routing is a short ordered handoff. Actual sending runs concurrently on each
// platform signal; preserving admission order here prevents routing reordering.
var NotificationRequested = signal.New[Notification, NotificationReceipt]("notification.requested", signal.Options[Notification]{Required: true, Single: true, Concurrency: 1})

// NewNotification freezes public origin facts; platform-owned extensions already
// follow contextinfo's immutable snapshot convention. It performs no sending.
func NewNotification(ctx context.Context, level slog.Level, text string) Notification {
	n := Notification{Text: text, Level: level}
	if origin, ok := contextinfo.ConversationFromContext(ctx); ok {
		n.Origin = &origin
	}
	return n
}
