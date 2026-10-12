package media

import (
	"context"
	"elbot/internal/events"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"elbot/internal/storage"
)

// OrphanGrace starts when the last reference is released, not at import time.
const OrphanGrace = time.Hour

const cleanupConcurrency = 4

// Cleanup preserves each deletion's source result before joining business errors.
// A cancellation cannot replace an independently failed deletion in LogResult.
func (m *Manager) Cleanup(ctx context.Context) (logResult events.LogResult, err error) {
	cleanupID := storage.NewID()
	stage := "lock"
	defer func() {
		lines := []string{"cleanup_id=" + cleanupID}
		if stage != "delete" {
			lines = append(lines, "stage="+stage)
		}
		lines = append(lines, strings.Split(logResult.Content, "\n")...)
		if len(lines) > 50 {
			omitted := len(lines) - 49
			lines = append(lines[:49], fmt.Sprintf("truncated=true omitted_details=%d", omitted))
		}
		logResult.Content = strings.Join(lines, "\n")
	}()
	// Only cleanup rounds serialize here; media operations use per-ID locks.
	unlock, err := m.cleanupMu.acquire(ctx, "cleanup")
	if err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("error=%q", err.Error())}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("error=%q", err.Error())}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	stage = "history_reconcile"
	if err := m.ReconcileHistory(ctx); err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("error=%q", err.Error())}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	stage = "expire_outputs"
	if err := m.Store.Media().ExpireOutputs(ctx, m.Now()); err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("error=%q", err.Error())}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	stage = "reference_check"
	issues, err := m.Store.Media().CheckReferences(ctx)
	if err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("error=%q", err.Error())}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	if len(issues) > 0 {
		err := fmt.Errorf("media reference inconsistencies prevent cleanup: %v", issues)
		return events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("issues=%d\n", len(issues)) + strings.Join(issues, "\n")}, err
	}
	stage = "claim"
	items, err := m.Store.Media().ClaimOrphans(ctx, m.Now().Add(-OrphanGrace))
	if err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("error=%q", err.Error())}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	stage = "delete"
	// Each worker owns a distinct result slot; errors are joined after all exit.
	failures := make([]error, len(items))
	results := make([]events.LogResult, len(items))
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
				results[i], failures[i] = m.deleteObject(ctx, items[i].ID)
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
	counts := map[events.ResultStatus]int{}
	for _, result := range results {
		counts[result.ResultStatus]++
		if result.ResultStatus == events.ResultFailed || (result.ResultStatus == events.ResultCanceled && resultStatus != events.ResultFailed) {
			resultStatus = result.ResultStatus
		}
	}
	lines := []string{fmt.Sprintf("claimed=%d deleted=%d skipped=%d failed=%d canceled=%d not_started=%d",
		len(items), counts[events.ResultSucceeded], counts[events.ResultSkipped], counts[events.ResultFailed], counts[events.ResultCanceled], counts[events.ResultUnspecified])}
	if err := ctx.Err(); err != nil {
		if resultStatus != events.ResultFailed {
			resultStatus = events.ResultFailed
			if err == context.Canceled {
				resultStatus = events.ResultCanceled
			}
		}
		lines = append(lines, fmt.Sprintf("cleanup_error=%q", err.Error()))
	}
	for _, status := range []events.ResultStatus{events.ResultFailed, events.ResultCanceled, events.ResultUnspecified, events.ResultSkipped, events.ResultSucceeded} {
		for i, result := range results {
			if result.ResultStatus != status {
				continue
			}
			if status == events.ResultUnspecified {
				lines = append(lines, fmt.Sprintf("media_id=%s result=not_started backend=%s", items[i].ID, items[i].Backend))
			} else {
				lines = append(lines, result.Content)
			}
		}
	}
	return events.LogResult{ResultStatus: resultStatus, Content: strings.Join(lines, "\n")}, errors.Join(append(failures, ctx.Err())...)
}

func (m *Manager) deleteObject(ctx context.Context, id string) (logResult events.LogResult, err error) {
	stage, backend := "lock", ""
	defer func() {
		if err != nil {
			logResult.ResultStatus = events.ResultFailed
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = events.ResultCanceled
			}
		}
		result := logResult.ResultStatus.String()
		if logResult.ResultStatus == events.ResultSucceeded {
			result = "deleted"
		}
		content := fmt.Sprintf("media_id=%s result=%s", id, result)
		if backend != "" {
			content += " backend=" + backend
		}
		if err != nil {
			content += fmt.Sprintf(" stage=%s error=%q", stage, err.Error())
		} else if logResult.Content != "" {
			content += " " + logResult.Content
		}
		logResult.Content = content
	}()
	unlock, err := m.objects.acquire(ctx, id)
	if err != nil {
		return logResult, err
	}
	defer unlock()
	// Read under the object lock rather than deleting from an old snapshot.
	stage = "read"
	item, err := m.Store.Media().Get(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return events.LogResult{ResultStatus: events.ResultSkipped, Content: "reason=unavailable"}, nil
	}
	if err != nil {
		return logResult, err
	}
	backend = item.Backend
	if !item.Deleting {
		return events.LogResult{ResultStatus: events.ResultSkipped, Content: "reason=unclaimed"}, nil
	}
	stage = "backend"
	primary, err := m.backendForStoredMedia(ctx, item)
	if err != nil {
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
			return logResult, fmt.Errorf("delete media %q: %w", id, err)
		}
	}
	if remoteBackend != nil {
		stage = "remote_delete"
		if err := remoteBackend.Remove(ctx, item); err != nil {
			return logResult, fmt.Errorf("delete remote media %q: %w", id, err)
		}
	}
	if localBackend != nil {
		stage = "local_delete"
		if err := localBackend.Remove(ctx, item); err != nil {
			return logResult, fmt.Errorf("delete local media %q: %w", id, err)
		}
	}
	stage = "finish_delete"
	if err := m.Store.Media().FinishDelete(ctx, id); err != nil {
		return logResult, fmt.Errorf("finish deleting media %q: %w", id, err)
	}
	return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}
