package chat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/storage"
)

func (r *Loop) consumeContextCompactSeed(ctx context.Context, session *storage.Session) {
	if r.Contexts == nil || session == nil {
		return
	}
	latest, err := r.Contexts.ConsumeSeed(ctx, session.ID)
	if err != nil {

		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "consume_compact_context_failed",
				Module:       "agent",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("consume compact context failed\nerror=%v", err),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			if errors.Is(err, context.Canceled) {
				record.ResultStatus = globalevents.ResultCanceled
				record.Level = slog.LevelInfo
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			record.Identity.SessionID = &session.ID
			_ = globalevents.EmitLog(ctx, record)
		}

		return
	}
	session.Metadata = latest.Metadata
}
