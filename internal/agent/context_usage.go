package agent

import (
	"context"

	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

func (a *Agent) ContextStatus(ctx context.Context, row *storage.Session) string {
	return a.contexts.Status(ctx, a.usageForSession(row), a.models.ResolveMode(row.Mode).ModelSelection)
}
func (a *Agent) recordUsage(id string, usage *llm.Usage) {
	if err := a.contexts.RecordUsage(context.Background(), id, usage); err != nil && a.logger != nil {
		a.logger.Warn("persist usage failed", "session_id", id, "error", err)
	}
}
func (a *Agent) usageForSession(row *storage.Session) *llm.Usage {
	usage, err := a.contexts.Usage(row)
	if err != nil && a.logger != nil {
		a.logger.Warn("load usage failed", "session_id", row.ID, "error", err)
	}
	return usage
}
func (a *Agent) shouldCompact(ctx context.Context, row *storage.Session, selection modelmgr.Selection) bool {
	return row != nil && a.contexts.ReachedCompactThreshold(ctx, a.usageForSession(row), selection.ModelSelection)
}
