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
		backend := m.Backend
		if item.Backend == "local" {
			backend = &LocalBackend{Root: m.Root}
		}
		if err := backend.Remove(ctx, item); err != nil {
			failures = append(failures, err)
			continue
		}
		if item.Backend == "local" && item.ObjectKey != "" && m.Remote != nil {
			if err := m.Remote.Remove(ctx, item); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		if err := m.Store.Media().FinishDelete(ctx, item.ID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
