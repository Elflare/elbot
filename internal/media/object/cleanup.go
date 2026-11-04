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

// Cleanup joins business errors after workers finish; each deletion logs its own outcome.
func (m *Manager) Cleanup(ctx context.Context, history History) (err error) {
	stage := "lock"
	var lines []string
	defer func() {
		record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelInfo, Module: "media", EventName: "media_cleanup", ResultStatus: events.ResultSucceeded, ContentLimit: 8192}
		if stage != "delete" {
			lines = append([]string{"stage=" + stage}, lines...)
		}
		if err != nil {
			record.Level, record.ResultStatus = slog.LevelError, events.ResultFailed
			if stage == "delete" {
				record.Level = slog.LevelWarn
			}
			if events.LogStatus(err) == events.ResultCanceled {
				record.Level, record.ResultStatus = slog.LevelInfo, events.ResultCanceled
			}
			lines = append(lines, fmt.Sprintf("error=%q", err.Error()))
		}
		if len(lines) > 50 {
			lines = append(lines[:49], fmt.Sprintf("truncated=true omitted_details=%d", len(lines)-49))
		}
		record.Content = strings.Join(lines, "\n")
		_ = events.EmitLog(ctx, record)
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
	failures := make([]error, len(items))
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
				deleted[i], failures[i] = m.deleteObject(ctx, items[i].ID)
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
		case events.LogStatus(failures[i]) == events.ResultCanceled:
			canceled++
		case failures[i] != nil:
			failed++
		case deleted[i]:
			removed++
		default:
			skipped++
		}
	}
	lines = append(lines, fmt.Sprintf("claimed=%d deleted=%d skipped=%d failed=%d canceled=%d not_started=%d", len(items), removed, skipped, failed, canceled, pending))
	return errors.Join(append(failures, ctx.Err())...)
}

func (m *Manager) deleteObject(ctx context.Context, id string) (deleted bool, err error) {
	stage, backend := "lock", ""
	level := slog.LevelError
	defer func() {
		record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelDebug, Module: "media", EventName: "media_cleanup", ResultStatus: events.ResultSkipped}
		result := "skipped"
		if deleted {
			record.ResultStatus = events.ResultSucceeded
			result = "deleted"
		}
		if err != nil {
			record.Level, record.ResultStatus = level, events.ResultFailed
			result = "failed"
			if events.LogStatus(err) == events.ResultCanceled {
				record.Level, record.ResultStatus = slog.LevelInfo, events.ResultCanceled
				result = "canceled"
			}
		}
		record.Content = fmt.Sprintf("media_id=%s result=%s backend=%s stage=%s", id, result, backend, stage)
		if err != nil {
			record.Content += fmt.Sprintf(" error=%q", err.Error())
		}
		_ = events.EmitLog(ctx, record)
	}()
	unlock, err := m.objects.acquire(ctx, id)
	if err != nil {
		return false, err
	}
	defer unlock()
	stage = "read"
	item, err := m.media.Get(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	backend = item.Backend
	if !item.Deleting {
		return false, nil
	}
	if item.ObjectKey != "" {
		stage = "remote_delete"
		if m.remote == nil {
			return false, fmt.Errorf("remote media backend is unavailable")
		}
		if err := m.remote.Remove(ctx, item.ObjectKey); err != nil {
			level = slog.LevelWarn
			return false, fmt.Errorf("delete remote media %q: %w", id, err)
		}
	}
	if item.LocalPath != "" {
		stage = "local_delete"
		if err := m.local.Remove(ctx, item.LocalPath); err != nil {
			return false, fmt.Errorf("delete local media %q: %w", id, err)
		}
	}
	stage = "finish_delete"
	if err := m.media.FinishDelete(ctx, id); err != nil {
		return false, fmt.Errorf("finish deleting media %q: %w", id, err)
	}
	return true, nil
}
