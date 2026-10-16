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
	// Cache only confirmed owners for this reconciliation, including missing rows.
	owners := map[[3]string]string{}
	for {
		items, err := m.Store.Media().ListHistory(ctx, after, pageSize)
		if err != nil {
			return err
		}
		for _, item := range items {
			key := [3]string{item.Platform, item.ScopeID, item.MessageID}
			owner, known := owners[key]
			if !known {
				row, err := m.History.GetByPlatformMessage(ctx, item.Platform, item.ScopeID, item.MessageID)
				if err != nil && !errors.Is(err, storage.ErrNotFound) {
					return fmt.Errorf("check history media owner: %w", err)
				}
				if err == nil {
					owner = row.ID
				}
				owners[key] = owner
			}
			if owner != item.HistoryID {
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
