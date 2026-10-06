package chat

import (
	"context"

	"elbot/internal/storage"
)

func (r *Loop) consumeContextCompactSeed(ctx context.Context, session *storage.Session) {
	if r.Contexts == nil || session == nil {
		return
	}
	latest, err := r.Contexts.ConsumeSeed(ctx, session.ID)
	if err != nil {
		if r.Logger != nil {
			r.Logger.WarnContext(ctx, "consume compact context failed", "session_id", session.ID, "error", err)
		}
		return
	}
	session.Metadata = latest.Metadata
}
