package media

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// OrphanGrace starts when the last reference is released, not at import time.
const OrphanGrace = time.Hour

func (m *Manager) Cleanup(ctx context.Context) error {
	m.objects.Lock()
	defer m.objects.Unlock()
	if err := m.ReconcileHistory(ctx); err != nil {
		return err
	}
	if err := m.Store.Media().ExpireOutputs(ctx, m.Now()); err != nil {
		return err
	}
	issues, err := m.Store.Media().CheckReferences(ctx)
	if err != nil {
		return err
	}
	if len(issues) > 0 {
		return fmt.Errorf("media reference inconsistencies prevent cleanup: %v", issues)
	}
	items, err := m.Store.Media().ClaimOrphans(ctx, m.Now().Add(-OrphanGrace))
	if err != nil {
		return err
	}
	var failures []error
	for i := range items {
		item := &items[i]
		primary, err := m.backendForStoredMedia(item)
		if err != nil {
			failures = append(failures, fmt.Errorf("delete media %q: %w", item.ID, err))
			continue
		}
		var localBackend, remoteBackend Backend
		if item.Backend == "local" {
			localBackend = primary
		} else {
			remoteBackend = primary
		}
		if item.ObjectKey != "" && remoteBackend == nil {
			remoteBackend, err = m.remoteBackend()
			if err != nil {
				failures = append(failures, fmt.Errorf("delete media %q: %w", item.ID, err))
				continue
			}
		}
		if remoteBackend != nil {
			if err := remoteBackend.Remove(ctx, item); err != nil {
				failures = append(failures, fmt.Errorf("delete remote media %q: %w", item.ID, err))
				continue
			}
		}
		if localBackend != nil {
			if err := localBackend.Remove(ctx, item); err != nil {
				failures = append(failures, fmt.Errorf("delete local media %q: %w", item.ID, err))
				continue
			}
		}
		if err := m.Store.Media().FinishDelete(ctx, item.ID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
