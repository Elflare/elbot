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
	usage, err := c.deps.Status.Usage(row)
	if err != nil {
		record := globalevents.LogRecord{
			Category:     globalevents.LogCategoryLog,
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

// ReachedCompactThreshold 统一按本会话已计量的用量和当前模型窗口判断临限。
func (c *ExecutionCoordinator) ReachedCompactThreshold(ctx context.Context, row *storage.Session, selection modelmgr.Selection) bool {
	return row != nil && c.deps.Contexts.ReachedCompactThreshold(ctx, c.usageForSession(ctx, row), selection)
}
