package chat

import (
	"context"

	"elbot/internal/storage"
)

func (r *Loop) consumeContextCompactSeed(ctx context.Context, session *storage.Session) error {
	latest, err := r.Contexts.ConsumeSeed(ctx, session.ID)
	if err != nil {
		return err
	}
	session.Metadata = latest.Metadata
	return nil
}
