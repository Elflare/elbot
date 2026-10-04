package background

import (
	"context"
	"errors"

	"elbot/internal/session"
	"elbot/internal/storage"
)

func SessionTakenOver(ctx context.Context, store storage.Store, id string) (bool, error) {
	if id == "" || store == nil || store.Sessions() == nil {
		return false, nil
	}
	row, err := store.Sessions().Get(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return session.WasPromoted(row), nil
}
