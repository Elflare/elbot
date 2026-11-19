package share

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
)

// EmitAgentLog copies only the source's explicit execution identity.
func EmitAgentLog(ctx context.Context, execution contextinfo.Execution, record events.LogRecord) {
	record.Identity = events.LogIdentity{SessionID: execution.SessionID, RunID: execution.RunID, Attempt: execution.Attempt, RequestID: execution.RequestID, RootRequestID: execution.RootRequestID}
	events.Log(context.WithoutCancel(ctx), record)
}
