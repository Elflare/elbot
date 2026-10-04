package agent

import (
	"context"

	"elbot/internal/storage"
)

func (a *Agent) consumeContextCompactSeed(ctx context.Context, session *storage.Session) {
	if a.store == nil || session == nil {
		return
	}
	latest, err := a.contexts.ConsumeSeed(ctx, session.ID)
	if err != nil {
		a.logContextCompactSeedError(ctx, session.ID, err)
		return
	}
	session.Metadata = latest.Metadata
}

func (a *Agent) logContextCompactSeedError(ctx context.Context, sessionID string, err error) {
	if a.logger != nil {
		a.logger.WarnContext(ctx, "consume compact context failed", "session_id", sessionID, "error", err)
	}
}
