package agent

import (
	"context"

	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

func (c *executionCoordinator) recordUsage(id string, usage *llm.Usage) {
	if err := c.contexts.RecordUsage(context.Background(), id, usage); err != nil && c.logger != nil {
		c.logger.Warn("persist usage failed", "session_id", id, "error", err)
	}
}
func (c *executionCoordinator) usageForSession(row *storage.Session) *llm.Usage {
	usage, err := c.contexts.Usage(row)
	if err != nil && c.logger != nil {
		c.logger.Warn("load usage failed", "session_id", row.ID, "error", err)
	}
	return usage
}
func (c *executionCoordinator) shouldCompact(ctx context.Context, row *storage.Session, selection modelmgr.Selection) bool {
	return row != nil && c.contexts.ReachedCompactThreshold(ctx, c.usageForSession(row), selection.ModelSelection)
}
