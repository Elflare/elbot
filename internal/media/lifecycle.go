package media

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"elbot/internal/storage"
)

// OrphanGrace starts when the last reference is released, not at import time.
const OrphanGrace = time.Hour

const cleanupConcurrency = 4

func (m *Manager) Cleanup(ctx context.Context) error {
	// Only cleanup rounds serialize here; media operations use per-ID locks.
	m.cleanupMu.Lock()
	defer m.cleanupMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
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
	// Each worker owns a distinct result slot; errors are joined after all exit.
	failures := make([]error, len(items))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(cleanupConcurrency, len(items)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				failures[i] = m.deleteObject(ctx, items[i].ID)
			}
		}()
	}
dispatch:
	for i := range items {
		if ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- i:
		}
	}
	close(jobs)
	workers.Wait()
	return errors.Join(append(failures, ctx.Err())...)
}

func (m *Manager) deleteObject(ctx context.Context, id string) error {
	unlock, err := m.objects.acquire(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	// Read under the object lock rather than deleting from an old snapshot.
	item, err := m.Store.Media().Get(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !item.Deleting {
		return nil
	}
	primary, err := m.backendForStoredMedia(ctx, item)
	if err != nil {
		return fmt.Errorf("delete media %q: %w", id, err)
	}
	var localBackend, remoteBackend Backend
	if item.Backend == "local" {
		localBackend = primary
	} else {
		remoteBackend = primary
	}
	if item.ObjectKey != "" && remoteBackend == nil {
		remoteBackend, err = m.remoteBackend(ctx)
		if err != nil {
			return fmt.Errorf("delete media %q: %w", id, err)
		}
	}
	if remoteBackend != nil {
		if err := remoteBackend.Remove(ctx, item); err != nil {
			return fmt.Errorf("delete remote media %q: %w", id, err)
		}
	}
	if localBackend != nil {
		if err := localBackend.Remove(ctx, item); err != nil {
			return fmt.Errorf("delete local media %q: %w", id, err)
		}
	}
	if err := m.Store.Media().FinishDelete(ctx, id); err != nil {
		return fmt.Errorf("finish deleting media %q: %w", id, err)
	}
	return nil
}
