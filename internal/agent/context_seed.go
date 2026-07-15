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
	latest, err := a.store.Sessions().Get(ctx, session.ID)
	if err != nil {
		a.logContextCompactSeedError(ctx, session.ID, err)
		return
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	if metadata.ContextCompact == nil || !metadata.ContextCompact.Pending {
		session.Metadata = latest.Metadata
		return
	}
	metadata.ContextCompact.Pending = false
	latest.Metadata = encodeSessionMetadataInto(latest.Metadata, metadata)
	latest.UpdatedAt = storage.Now()
	if err := a.store.Sessions().Update(ctx, latest); err != nil {
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
