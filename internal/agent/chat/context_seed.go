package chat

import (
	"context"
	"log/slog"

	globalevents "elbot/internal/events"
	"elbot/internal/storage"
)

func (r *Loop) consumeContextCompactSeed(ctx context.Context, session *storage.Session) {
	if r.Contexts == nil || session == nil {
		return
	}
	latest, err := r.Contexts.ConsumeSeed(ctx, session.ID)
	if err != nil {

		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelError,
			Name:     "consume_compact_context_failed",
			Module:   "agent",
			Summary:  "consume compact context failed",
			Fields:   []slog.Attr{slog.Any("session_id", session.ID), slog.Any("error", err)},
		})

		return
	}
	session.Metadata = latest.Metadata
}
