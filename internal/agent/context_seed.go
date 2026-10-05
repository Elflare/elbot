package agent

import (
	"context"

	"elbot/internal/storage"
)

func (r *chatRunner) consumeContextCompactSeed(ctx context.Context, session *storage.Session) {
	if r.contexts == nil || session == nil {
		return
	}
	latest, err := r.contexts.ConsumeSeed(ctx, session.ID)
	if err != nil {
		r.logContextCompactSeedError(ctx, session.ID, err)
		return
	}
	session.Metadata = latest.Metadata
}

func (r *chatRunner) logContextCompactSeedError(ctx context.Context, sessionID string, err error) {
	if r.logger != nil {
		r.logger.WarnContext(ctx, "consume compact context failed", "session_id", sessionID, "error", err)
	}
}
