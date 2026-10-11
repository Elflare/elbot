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
