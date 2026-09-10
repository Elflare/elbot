package media

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/storage"
)

// ReconcileHistory releases only associations whose historical owner is confirmed gone.
// An unavailable history database is not evidence that its messages were deleted.
func (m *Manager) ReconcileHistory(ctx context.Context) error {
	if m.History == nil {
		return nil
	}
	const pageSize = 100
	after := ""
	for {
		items, err := m.Store.Media().ListHistory(ctx, after, pageSize)
		if err != nil {
			return err
		}
		for _, item := range items {
			row, err := m.History.GetByPlatformMessage(ctx, item.Platform, item.ScopeID, item.MessageID)
			if err != nil && !errors.Is(err, storage.ErrNotFound) {
				return fmt.Errorf("check history media owner: %w", err)
			}
			if errors.Is(err, storage.ErrNotFound) || row.ID != item.HistoryID {
				if err := m.Store.Media().DeleteHistory(ctx, item.OwnerID); err != nil {
					return err
				}
			}
		}
		if len(items) < pageSize {
			return nil
		}
		after = items[len(items)-1].OwnerID
	}
}
