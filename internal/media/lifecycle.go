package media

import (
	"context"
	"elbot/internal/events"
	"errors"
	"fmt"
	"sync"
	"time"

	"elbot/internal/storage"
)

// OrphanGrace starts when the last reference is released, not at import time.
const OrphanGrace = time.Hour

const cleanupConcurrency = 4

// Cleanup preserves each deletion's source result before joining business errors.
// A cancellation cannot replace an independently failed deletion in LogResult.
func (m *Manager) Cleanup(ctx context.Context) (events.LogResult, error) {
	// Only cleanup rounds serialize here; media operations use per-ID locks.
	unlock, err := m.cleanupMu.acquire(ctx, "cleanup")
	if err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	if err := m.ReconcileHistory(ctx); err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	if err := m.Store.Media().ExpireOutputs(ctx, m.Now()); err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	issues, err := m.Store.Media().CheckReferences(ctx)
	if err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	if len(issues) > 0 {
		err := fmt.Errorf("media reference inconsistencies prevent cleanup: %v", issues)
		return events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}, err
	}
	items, err := m.Store.Media().ClaimOrphans(ctx, m.Now().Add(-OrphanGrace))
	if err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	// Each worker owns a distinct result slot; errors are joined after all exit.
	failures := make([]error, len(items))
	logResult := make([]events.LogResult, len(items))
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
				logResult[i], failures[i] = m.deleteObject(ctx, items[i].ID)
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
	resultStatus := events.ResultSucceeded
	content := fmt.Sprintf("media cleanup: claimed=%d", len(items))
	for _, result := range logResult {
		if result.ResultStatus == events.ResultFailed || (result.ResultStatus == events.ResultCanceled && resultStatus != events.ResultFailed) {
			resultStatus = result.ResultStatus
		}
		if result.Content != "" {
			content += "\n" + result.Content
		}
	}
	if err := ctx.Err(); err != nil {
		if resultStatus != events.ResultFailed {
			resultStatus = events.ResultFailed
			if err == context.Canceled {
				resultStatus = events.ResultCanceled
			}
		}
		content += fmt.Sprintf("\ncleanup_error=%v", err)
	}
	return events.LogResult{ResultStatus: resultStatus, Content: content}, errors.Join(append(failures, ctx.Err())...)
}

func (m *Manager) deleteObject(ctx context.Context, id string) (events.LogResult, error) {
	unlock, err := m.objects.acquire(ctx, id)
	if err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("media_id=%s error=%v", id, err)}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	defer unlock()
	// Read under the object lock rather than deleting from an old snapshot.
	item, err := m.Store.Media().Get(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return events.LogResult{ResultStatus: events.ResultSkipped, Content: fmt.Sprintf("media_id=%s object no longer needs deletion", id)}, nil
	}
	if err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("media_id=%s error=%v", id, err)}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	if !item.Deleting {
		return events.LogResult{ResultStatus: events.ResultSkipped, Content: fmt.Sprintf("media_id=%s object no longer needs deletion", id)}, nil
	}
	primary, err := m.backendForStoredMedia(ctx, item)
	if err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("media_id=%s error=%v", id, err)}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, fmt.Errorf("delete media %q: %w", id, err)
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
			logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("media_id=%s error=%v", id, err)}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = events.ResultCanceled
			}
			return logResult, fmt.Errorf("delete media %q: %w", id, err)
		}
	}
	if remoteBackend != nil {
		if err := remoteBackend.Remove(ctx, item); err != nil {
			logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("media_id=%s error=%v", id, err)}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = events.ResultCanceled
			}
			return logResult, fmt.Errorf("delete remote media %q: %w", id, err)
		}
	}
	if localBackend != nil {
		if err := localBackend.Remove(ctx, item); err != nil {
			logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("media_id=%s error=%v", id, err)}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = events.ResultCanceled
			}
			return logResult, fmt.Errorf("delete local media %q: %w", id, err)
		}
	}
	if err := m.Store.Media().FinishDelete(ctx, id); err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("media_id=%s error=%v", id, err)}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, fmt.Errorf("finish deleting media %q: %w", id, err)
	}
	return events.LogResult{ResultStatus: events.ResultSucceeded, Content: fmt.Sprintf("media_id=%s object deleted", id)}, nil
}
