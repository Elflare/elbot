package storage

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCronRunStatePreservesDefinitionAndAtomicallyCounts(t *testing.T) {
	store := newTestStore(t)
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
