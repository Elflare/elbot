package agent

import (
	"context"
	"log/slog"

	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

func (c *executionCoordinator) recordUsage(ctx context.Context, id string, usage *llm.Usage) {
	if err := c.contexts.RecordUsage(context.WithoutCancel(ctx), id, usage); err != nil {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelError,
			Name:     "persist_usage_failed",
			Module:   "agent",
			Summary:  "persist usage failed",
			Fields:   []slog.Attr{slog.Any("session_id", id), slog.Any("error", err)},
		})
	}
}
func (c *executionCoordinator) usageForSession(ctx context.Context, row *storage.Session) *llm.Usage {
	usage, err := c.contexts.Usage(row)
	if err != nil {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelError,
			Name:     "load_usage_failed",
			Module:   "agent",
			Summary:  "load usage failed",
			Fields:   []slog.Attr{slog.Any("session_id", row.ID), slog.Any("error", err)},
		})
	}
	return usage
}
func (c *executionCoordinator) shouldCompact(ctx context.Context, row *storage.Session, selection modelmgr.Selection) bool {
	return row != nil && c.contexts.ReachedCompactThreshold(ctx, c.usageForSession(ctx, row), selection.ModelSelection)
}
