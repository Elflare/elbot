package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCronRunStatePreservesDefinitionAndAtomicallyCounts(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := t.Context()
	next := time.Now().Add(time.Hour).Truncate(time.Second)
	job, err := store.UpsertCronJob(ctx, UpsertCronJobRequest{Name: "job", Handler: "handler", Schedule: "0 4 * * *", Enabled: true, Metadata: `{"title":"new"}`, NextRunAt: &next})
	if err != nil {
		t.Fatal(err)
	}
	state := CronJobRunState{LastRunAt: time.Now().Add(-time.Minute).Truncate(time.Second), UpdatedAt: time.Now().Truncate(time.Second)}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.UpdateRunState(ctx, job.ID, state); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := store.GetByName(ctx, job.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunCount != 12 || !got.Enabled || got.NextRunAt == nil || !got.NextRunAt.Equal(next) || got.Schedule != job.Schedule || got.Metadata != job.Metadata || got.LastRunAt == nil || !got.LastRunAt.Equal(state.LastRunAt) {
		t.Fatalf("record=%#v", got)
	}
	if err := store.DisableByName(ctx, job.Name); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunState(ctx, job.ID, state); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetByName(ctx, job.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.NextRunAt != nil || got.RunCount != 13 {
		t.Fatalf("disabled state overwritten: %#v", got)
	}
	if err := store.DeleteByName(ctx, job.Name); err != nil {
		t.Fatal(err)
	}
	created, err := store.UpsertCronJob(ctx, UpsertCronJobRequest{Name: job.Name, Handler: job.Handler, Schedule: job.Schedule, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunState(ctx, job.ID, state); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old run=%v", err)
	}
	got, err = store.GetByName(ctx, job.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.ID == job.ID || got.RunCount != 0 || got.LastRunAt != nil {
		t.Fatalf("recreated task received old run: %#v", got)
	}
}

func TestCronJobRepositoryUpsertAndRunState(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := context.Background()

	initialNextRun := Now().Add(30 * time.Minute)
	job, err := store.UpsertCronJob(ctx, UpsertCronJobRequest{
		Name:      "system.maintenance.cleanup",
		Handler:   "maintenance.cleanup",
		Schedule:  "0 3 * * *",
		Enabled:   true,
		Metadata:  `{"system":true}`,
		NextRunAt: &initialNextRun,
	})
	if err != nil {
		t.Fatalf("upsert cron job: %v", err)
	}
	if job.ID == "" || !job.Enabled || job.RunCount != 0 || job.NextRunAt == nil {
		t.Fatalf("job = %#v", job)
	}

	updatedNextRun := initialNextRun.Add(time.Hour)
	updated, err := store.UpsertCronJob(ctx, UpsertCronJobRequest{
		Name:      job.Name,
		Handler:   job.Handler,
		Schedule:  "0 4 * * *",
		Enabled:   true,
		Metadata:  `{"system":true,"updated":true}`,
		NextRunAt: &updatedNextRun,
	})
	if err != nil {
		t.Fatalf("update cron job: %v", err)
	}
	if updated.ID != job.ID || updated.Schedule != "0 4 * * *" || updated.NextRunAt == nil || !updated.NextRunAt.Equal(updatedNextRun) {
		t.Fatalf("updated = %#v", updated)
	}

	enabled, err := store.ListCronJobs(ctx, false)
	if err != nil {
		t.Fatalf("list enabled cron jobs: %v", err)
	}
	if len(enabled) != 1 || enabled[0].Name != job.Name {
		t.Fatalf("enabled = %#v", enabled)
	}

	lastRun := Now()
	if err := store.UpdateRunState(ctx, job.ID, CronJobRunState{
		LastRunAt: lastRun,
		UpdatedAt: lastRun,
	}); err != nil {
		t.Fatalf("update run state: %v", err)
	}
	got, err := store.GetByName(ctx, job.Name)
	if err != nil {
		t.Fatalf("get cron job: %v", err)
	}
	if got.RunCount != 1 || got.LastRunAt == nil || got.NextRunAt == nil {
		t.Fatalf("got = %#v", got)
	}

	if err := store.DisableByName(ctx, job.Name); err != nil {
		t.Fatalf("disable cron job: %v", err)
	}
	listed, err := store.ListCronJobs(ctx, true)
	if err != nil {
		t.Fatalf("list all cron jobs: %v", err)
	}
	if len(listed) != 1 || listed[0].Name != job.Name || listed[0].Enabled {
		t.Fatalf("listed after disable = %#v", listed)
	}
	if listed[0].NextRunAt != nil {
		t.Fatalf("next run was not cleared after disable: %#v", listed[0])
	}
	enabled, err = store.ListCronJobs(ctx, false)
	if err != nil {
		t.Fatalf("list enabled after disable: %v", err)
	}
	if len(enabled) != 0 {
		t.Fatalf("enabled after disable = %#v", enabled)
	}
	if err := store.DeleteByName(ctx, job.Name); err != nil {
		t.Fatalf("delete cron job: %v", err)
	}
	_, err = store.GetByName(ctx, job.Name)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted cron job error = %v", err)
	}
}

func TestCronJobRepositoryDeliveryCASAndReset(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := context.Background()
	req := UpsertCronJobRequest{Name: "user.cron.delivery", Handler: "builtin.cron", Schedule: "0 3 * * *", Enabled: true, Metadata: `{"title":"old"}`}
	job, err := store.UpsertCronJob(ctx, req)
	if err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	lastRun := Now().Add(-time.Hour)
	if err := store.UpdateRunState(ctx, job.ID, CronJobRunState{
		LastRunAt: lastRun, UpdatedAt: lastRun,
	}); err != nil {
		t.Fatalf("save prior run state: %v", err)
	}

	swapped, err := store.CompareAndSwapDelivery(ctx, job.ID, "", "session-1", `{"report_ready":true}`, nil)
	if err != nil || !swapped {
		t.Fatalf("claim delivery: swapped=%v err=%v", swapped, err)
	}
	if swapped, err = store.CompareAndSwapDelivery(ctx, job.ID, "wrong", "session-2", `{}`, nil); err != nil || swapped {
		t.Fatalf("stale delivery swap: swapped=%v err=%v", swapped, err)
	}

	req.Metadata = `{"title":"new"}`
	preserved, err := store.UpsertCronJob(ctx, req)
	if err != nil {
		t.Fatalf("upsert preserving delivery: %v", err)
	}
	if preserved.DeliveryToken != "session-1" || preserved.DeliveryState != `{"report_ready":true}` {
		t.Fatalf("delivery was overwritten: %#v", preserved)
	}
	if preserved.ID != job.ID || preserved.Metadata != req.Metadata ||
		!preserved.CreatedAt.Equal(job.CreatedAt) || preserved.RunCount != 1 ||
		preserved.LastRunAt == nil || !preserved.LastRunAt.Equal(lastRun) {
		t.Fatalf("updated job lost configuration or run history: %#v", preserved)
	}

	req.ResetDelivery = true
	reset, err := store.UpsertCronJob(ctx, req)
	if err != nil {
		t.Fatalf("upsert resetting delivery: %v", err)
	}
	if reset.DeliveryToken != "" || reset.DeliveryState != "" {
		t.Fatalf("delivery was not reset: %#v", reset)
	}
	if reset.ID != job.ID || reset.RunCount != 1 || reset.LastRunAt == nil || !reset.LastRunAt.Equal(lastRun) {
		t.Fatalf("delivery reset changed run history: %#v", reset)
	}
	persisted, err := store.GetByName(ctx, job.Name)
	if err != nil {
		t.Fatalf("get reset job: %v", err)
	}
	if persisted.DeliveryToken != reset.DeliveryToken || persisted.DeliveryState != reset.DeliveryState ||
		persisted.Metadata != reset.Metadata || persisted.RunCount != reset.RunCount ||
		persisted.LastRunAt == nil || !persisted.LastRunAt.Equal(*reset.LastRunAt) {
		t.Fatalf("returned job differs from stored job: returned=%#v stored=%#v", reset, persisted)
	}
	if disabled, err := store.DisableByNameIfDeliveryToken(ctx, job.Name, "session-1"); err != nil || disabled {
		t.Fatalf("stale conditional disable: disabled=%v err=%v", disabled, err)
	}
}

func TestCronJobRepositoryUpdateMissingJob(t *testing.T) {
	store := NewStoreForTest(t)
	repo := store
	job, err := repo.update(context.Background(), &CronJob{ID: "missing", UpdatedAt: Now()}, false)
	if !errors.Is(err, ErrNotFound) || job != nil {
		t.Fatalf("update missing job: job=%#v err=%v", job, err)
	}
}
