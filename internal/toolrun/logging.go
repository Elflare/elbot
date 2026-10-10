package toolrun

import (
	"context"

	"elbot/internal/events"
)

// logTool publishes a complete source record. The source owns its identity,
// content, level and result; publication never changes business execution.
func logTool(ctx context.Context, record events.LogRecord) {
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
}
