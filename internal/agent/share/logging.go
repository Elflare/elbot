package share

import (
	"context"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/events"
)

// emitAgentLog copies only the source's explicit event identity.
func EmitAgentLog(ctx context.Context, meta agentevents.EventMeta, record events.LogRecord) {
	if meta.SessionID != "" {
		record.Identity.SessionID = &meta.SessionID
	}
	if meta.RunID != "" {
		record.Identity.RunID = &meta.RunID
	}
	if meta.Attempt != "" {
		record.Identity.Attempt = &meta.Attempt
	}
	if meta.RequestID != "" {
		record.Identity.RequestID = &meta.RequestID
	}
	if meta.RootRequestID != "" {
		record.Identity.RootRequestID = &meta.RootRequestID
	}
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
}
