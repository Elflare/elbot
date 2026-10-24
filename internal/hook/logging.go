// 适配 Hook 日志与通知的对外发布。

package hook

import (
	"context"

	"elbot/internal/events"
)

// LogHook publishes a complete source-owned record even after cancellation.
// Sources own identity, level, result and content; publication does not alter them.
func LogHook(ctx context.Context, record events.LogRecord) {
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
}

// notify publishes a complete source-owned notification. The producer chooses
// its destination, severity, text and cancellation policy; this adapter adds none.
func notify(ctx context.Context, notice events.Notification) error {
	_, err := events.NotificationRequested.EmitAsync(ctx, notice)
	return err
}
