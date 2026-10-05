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
		if r.logger != nil {
			r.logger.WarnContext(ctx, "consume compact context failed", "session_id", session.ID, "error", err)
		}
		return
	}
	session.Metadata = latest.Metadata
}
