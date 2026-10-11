package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

func (c *ExecutionCoordinator) recordUsage(ctx context.Context, id string, usage *llm.Usage) {
	if err := c.contexts.RecordUsage(context.WithoutCancel(ctx), id, usage); err != nil {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "persist_usage_failed",
				Module:       "agent",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("persist usage failed\nerror=%v", err),
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
			record.Identity.SessionID = &id
			_ = globalevents.EmitLog(ctx, record)
		}
	}
}
func (c *ExecutionCoordinator) usageForSession(ctx context.Context, row *storage.Session) *llm.Usage {
	usage, err := c.contexts.Usage(row)
	if err != nil {
		{
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
			record.Identity.SessionID = &row.ID
			_ = globalevents.EmitLog(ctx, record)
		}
	}
	return usage
}
func (c *ExecutionCoordinator) shouldCompact(ctx context.Context, row *storage.Session, selection modelmgr.Selection) bool {
	return row != nil && c.contexts.ReachedCompactThreshold(ctx, c.usageForSession(ctx, row), selection.ModelSelection)
}
