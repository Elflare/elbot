package events

import (
	"context"
	"errors"
	"fmt"
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
	Platform string               // Runtime platform name, e.g. cli or telegram.
	ChatType contextinfo.ChatType // Private, group or channel destination.
	ID       string               // Platform user, group or channel ID, according to ChatType.
}

// Routing is a short ordered handoff. Actual sending runs concurrently on each
// platform signal; preserving admission order here prevents routing reordering.
var NotificationRequested = signal.New[Notification, signal.Empty]("notification.requested", signal.Options[Notification]{Required: true, Single: true, Concurrency: 1})

// Notify publishes asynchronously. The caller owns destination and cancellation
// policy; admission failures are logged here, delivery failures by the consumer.
func Notify(ctx context.Context, notification Notification) {
	_, err := NotificationRequested.EmitAsync(ctx, notification)
	if err != nil && ctx.Err() == nil && !errors.Is(err, signal.ErrClosed) {
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		AsyncLog(ctx, LogRecord{
			Category: LogRuntime, Level: slog.LevelWarn, EventName: "notification_rejected", Module: "notification",
			Content: fmt.Sprintf("target=%+v error=%v", notification.Target, err), ContentLimit: 4096,
			Identity: LogIdentityFromExecution(facts), ResultStatus: ResultFailed,
		})
	}
}

// LogAndNotify publishes both independently, without waiting for either consumer.
func LogAndNotify(ctx context.Context, record LogRecord, notification Notification) {
	AsyncLog(ctx, record)
	Notify(ctx, notification)
}

// NewNotification freezes public origin facts; platform-owned extensions already
// follow contextinfo's immutable snapshot convention. It performs no sending.
func NewNotification(ctx context.Context, level slog.Level, text string) Notification {
	n := Notification{Text: text, Level: level}
	if origin, ok := contextinfo.ConversationFromContext(ctx); ok {
		n.Origin = &origin
	}
	return n
}
