package execution

import (
	"context"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
	"errors"
	"fmt"
	"log/slog"
)

func (c *ExecutionCoordinator) usageForSession(ctx context.Context, row *storage.Session) *llm.Usage {
	usage, err := c.status.Usage(row)
	if err != nil {
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelError,
			EventName:    "load_usage_failed",
			Module:       "agent",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("load usage failed\nerror=%v", err),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultFailed,
		}
		if errors.Is(err, context.Canceled) {
			record.ResultStatus = globalevents.ResultCanceled
			record.Level = slog.LevelInfo
		}
		record.Identity = globalevents.LogIdentityFromContext(ctx)
		record.Identity.SessionID = row.ID
		globalevents.Log(ctx, record)
	}
	return usage
}

func (c *ExecutionCoordinator) shouldCompact(ctx context.Context, row *storage.Session, selection modelmgr.Selection) bool {
	return row != nil && c.contexts.ReachedCompactThreshold(ctx, c.usageForSession(ctx, row), selection)
}
