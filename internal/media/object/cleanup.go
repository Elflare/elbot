package object

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"elbot/internal/events"
	"elbot/internal/storage"
)

// OrphanGrace starts when the last reference is released, not at import time.
const OrphanGrace = time.Hour

const cleanupConcurrency = 4

// Cleanup handles individual deletion failures and returns only failures that stop the batch.
func (m *Manager) Cleanup(ctx context.Context, history History) (err error) {
	var summary string
	defer func() {
		if err == nil {
			events.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelInfo, Module: "media", EventName: "media_cleanup", ResultStatus: events.ResultSucceeded, ContentLimit: 8192, Content: summary})
		}
	}()
	unlock, err := m.cleanupMu.acquire(ctx, "cleanup")
	if err != nil {
		return err
	}
	defer unlock()
	if err := m.retryReleases(ctx, false); err != nil {
		return err
	}
	if err := m.ReconcileHistory(ctx, history); err != nil {
		return err
	}
	if err := m.media.ExpireOutputs(ctx, m.options.Now()); err != nil {
		return err
	}
	issues, err := m.media.CheckReferences(ctx)
	if err != nil {
		return err
	}
	if len(issues) > 0 {
		return fmt.Errorf("media reference inconsistencies prevent cleanup: %v", issues)
	}
	items, err := m.media.ClaimOrphans(ctx, m.options.Now().Add(-OrphanGrace))
	if err != nil {
		return err
	}
	const (
		notStarted uint8 = iota
		deleted
		skipped
		failed
		canceled
		outcomeCount
	)
	outcomes := make([]uint8, len(items))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(cleanupConcurrency, len(items)) {
		workers.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				removed, err := m.deleteObject(ctx, items[i].ID)
				switch {
				case err == nil && removed:
					outcomes[i] = deleted
				case err == nil:
					outcomes[i] = skipped
				case errors.Is(err, ctx.Err()):
					outcomes[i] = canceled
					return
				default:
					outcomes[i] = failed
					events.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn,
						EventName: "media_cleanup", Module: "media",
						Content: fmt.Sprintf("media_id=%s error=%q", items[i].ID, err.Error()), ResultStatus: events.LogStatus(err)})
				}
			}
		})
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
	var counts [outcomeCount]int
	for _, outcome := range outcomes {
		counts[outcome]++
	}
	summary = fmt.Sprintf("claimed=%d deleted=%d skipped=%d failed=%d canceled=%d not_started=%d", len(items), counts[deleted], counts[skipped], counts[failed], counts[canceled], counts[notStarted])
	return ctx.Err()
}

func (m *Manager) deleteObject(ctx context.Context, id string) (deleted bool, err error) {
	unlock, err := m.objects.acquire(ctx, id)
	if err != nil {
		return false, err
	}
	defer unlock()
	item, err := m.media.Get(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !item.Deleting {
		return false, nil
	}
	if item.ObjectKey != "" {
		if m.remote == nil {
			return false, fmt.Errorf("remote media backend is unavailable")
		}
		if err := m.remote.Remove(ctx, item.ObjectKey); err != nil {
			return false, fmt.Errorf("delete remote media %q: %w", id, err)
		}
	}
	if item.LocalPath != "" {
		if err := m.local.Remove(ctx, item.LocalPath); err != nil {
			return false, fmt.Errorf("delete local media %q: %w", id, err)
		}
	}
	if err := m.media.FinishDelete(ctx, id); err != nil {
		return false, fmt.Errorf("finish deleting media %q: %w", id, err)
	}
	return true, nil
}
