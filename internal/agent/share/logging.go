package share

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
)

// EmitAgentLog copies only the source's explicit execution identity.
func EmitAgentLog(ctx context.Context, execution contextinfo.Execution, record events.LogRecord) {
	if execution.SessionID != "" {
		record.Identity.SessionID = &execution.SessionID
	}
	if execution.RunID != "" {
		record.Identity.RunID = &execution.RunID
	}
	if execution.Attempt != "" {
		record.Identity.Attempt = &execution.Attempt
	}
	if execution.RequestID != "" {
		record.Identity.RequestID = &execution.RequestID
	}
	if execution.RootRequestID != "" {
		record.Identity.RootRequestID = &execution.RootRequestID
	}
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
}
