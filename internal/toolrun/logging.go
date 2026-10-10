package toolrun

import (
	"context"

	"elbot/internal/events"
)

// logToolCall publishes the complete source record for a tool invocation.
// The caller owns its identity, body, level and result; publication never
// inspects business objects or changes execution/permission/cancellation rules.
func logToolCall(ctx context.Context, record events.LogRecord) {
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
}

// logToolOperation publishes a complete record for an actual preparation,
// output or persistence operation, independently of the invocation's result.
func logToolOperation(ctx context.Context, record events.LogRecord) {
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
}
