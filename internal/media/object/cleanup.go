package object

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	stage := "lock"
	var lines []string
	defer func() {
		if err != nil {
			return // Terminal failures belong to the maintenance task boundary.
		}
		record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelInfo, Module: "media", EventName: "media_cleanup", ResultStatus: events.ResultSucceeded, ContentLimit: 8192}
		if stage != "delete" {
			lines = append([]string{"stage=" + stage}, lines...)
		}
		if len(lines) > 50 {
			lines = append(lines[:49], fmt.Sprintf("truncated=true omitted_details=%d", len(lines)-49))
		}
		record.Content = strings.Join(lines, "\n")
		events.Log(ctx, record)
	}()
	unlock, err := m.cleanupMu.acquire(ctx, "cleanup")
	if err != nil {
		return err
	}
	defer unlock()
	stage = "release_owned"
	if err := m.retryReleases(ctx); err != nil {
		return err
	}
	stage = "history_reconcile"
	if err := m.ReconcileHistory(ctx, history); err != nil {
		return err
	}
	stage = "expire_outputs"
	if err := m.media.ExpireOutputs(ctx, m.options.Now()); err != nil {
		return err
	}
	stage = "reference_check"
	issues, err := m.media.CheckReferences(ctx)
	if err != nil {
		return err
	}
	if len(issues) > 0 {
		return fmt.Errorf("media reference inconsistencies prevent cleanup: %v", issues)
	}
	stage = "claim"
	items, err := m.media.ClaimOrphans(ctx, m.options.Now().Add(-OrphanGrace))
	if err != nil {
		return err
	}
	stage = "delete"
	failedItems, canceledItems := make([]bool, len(items)), make([]bool, len(items))
	deleted, started := make([]bool, len(items)), make([]bool, len(items))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(cleanupConcurrency, len(items)) {
		workers.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				started[i] = true
				var err error
				deleted[i], err = m.deleteObject(ctx, items[i].ID)
				if err != nil {
					if errors.Is(err, ctx.Err()) {
						canceledItems[i] = true
						return
					}
					failedItems[i] = true
					events.Log(ctx, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn,
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
	var removed, skipped, failed, canceled, pending int
	for i := range items {
		switch {
		case !started[i]:
			pending++
		case canceledItems[i]:
			canceled++
		case failedItems[i]:
			failed++
		case deleted[i]:
			removed++
		default:
			skipped++
		}
	}
	lines = append(lines, fmt.Sprintf("claimed=%d deleted=%d skipped=%d failed=%d canceled=%d not_started=%d", len(items), removed, skipped, failed, canceled, pending))
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
