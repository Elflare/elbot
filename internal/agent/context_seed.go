package agent

import (
	"context"

	"elbot/internal/storage"
)

func pendingContextCompact(session *storage.Session) *contextCompactState {
	if session == nil {
		return nil
	}
	compact := decodeSessionMetadata(session.Metadata).ContextCompact
	if compact == nil || !compact.Pending || compact.Summary == "" {
		return nil
	}
	return compact
}

func (a *Agent) consumeContextCompactSeed(ctx context.Context, session *storage.Session) {
	if a.store == nil || session == nil {
		return
	}
	latest, err := a.mutateSessionMetadata(ctx, session.ID, func(metadata *sessionMetadata) {
		if metadata.ContextCompact != nil {
			metadata.ContextCompact.Pending = false
		}
	})
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
