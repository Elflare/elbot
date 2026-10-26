package cron

import (
	"context"
	globalevents "elbot/internal/events"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"elbot/internal/storage"
)

type fakeCronRepo struct {
	mu       sync.Mutex
	jobs     map[string]*Job
	runs     map[string]JobRunState
	counts   map[string]int
	mappings []ReportMessageMap
}

func newFakeCronRepo() *fakeCronRepo {
	return &fakeCronRepo{jobs: map[string]*Job{}, runs: map[string]JobRunState{}, counts: map[string]int{}}
}

func (r *fakeCronRepo) Upsert(ctx context.Context, req UpsertJobRequest) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if job, ok := r.jobs[req.Name]; ok {
		job.Handler = req.Handler
		job.Schedule = req.Schedule
		job.Enabled = req.Enabled
		job.NextRunAt = req.NextRunAt
		return job, nil
	}
	job := &Job{ID: storage.NewID(), Name: req.Name, Handler: req.Handler, Schedule: req.Schedule, Enabled: req.Enabled, NextRunAt: req.NextRunAt}
	r.jobs[job.Name] = job
	return job, nil
}

func (r *fakeCronRepo) GetByName(ctx context.Context, name string) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[name]
	if !ok {
		return nil, ErrJobNotFound
	}
	copy := *job
	return &copy, nil
}

func (r *fakeCronRepo) List(ctx context.Context, includeDisabled bool) ([]Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	jobs := []Job{}
	for _, job := range r.jobs {
		if includeDisabled || job.Enabled {
			jobs = append(jobs, *job)
		}
	}
	return jobs, nil
}

func (r *fakeCronRepo) ListEnabled(ctx context.Context) ([]Job, error) {
	return r.List(ctx, false)
}

func (r *fakeCronRepo) UpdateNextRunAt(ctx context.Context, id string, nextRunAt *time.Time, updatedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, job := range r.jobs {
		if job.ID == id {
			job.NextRunAt = nextRunAt
			return nil
		}
	}
	return ErrJobNotFound
}

func (r *fakeCronRepo) UpdateRunState(ctx context.Context, id string, state JobRunState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, job := range r.jobs {
		if job.ID == id {
			r.runs[id] = state
			r.counts[id]++
			return nil
		}
	}
	return ErrJobNotFound
}

func (r *fakeCronRepo) CompareAndSwapDelivery(ctx context.Context, id, expectedToken, nextToken, deliveryState string, mappings []ReportMessageMap) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mappings = append(r.mappings, mappings...)
	for _, job := range r.jobs {
		if job.ID == id && job.Enabled && job.DeliveryToken == expectedToken {
			job.DeliveryToken = nextToken
			job.DeliveryState = deliveryState
			return true, nil
		}
	}
	return false, nil
}

func (r *fakeCronRepo) DisableByName(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[name]
	if !ok {
		return ErrJobNotFound
	}
	job.Enabled = false
	job.NextRunAt = nil
	return nil
}

func (r *fakeCronRepo) DisableByNameIfDeliveryToken(ctx context.Context, name, deliveryToken string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[name]
	if !ok || !job.Enabled || job.DeliveryToken != deliveryToken {
		return false, nil
	}
	job.Enabled = false
	job.NextRunAt = nil
	return true, nil
}

func (r *fakeCronRepo) DeleteByName(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.jobs[name]; !ok {
		return ErrJobNotFound
	}
	delete(r.jobs, name)
	return nil
}

func TestManagerRunsRegisteredHandlerAndUpdatesState(t *testing.T) {
	repo := newFakeCronRepo()
	manager := NewManager(repo)
	called := false
	if err := manager.RegisterHandler("test.handler", func(ctx context.Context, job Job) (globalevents.LogResult, error) {
		called = true
		if job.Name != "test.job" {
			t.Fatalf("job = %#v", job)
		}
		return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "finished"}, nil
	}); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	job, err := manager.UpsertJob(context.Background(), UpsertJobRequest{Name: "test.job", Handler: "test.handler", Schedule: "0 3 * * *", Enabled: true})
	if err != nil {
		t.Fatalf("upsert job: %v", err)
	}

	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { <-manager.Stop().Done() }()
	manager.runJob(job.Name)
	if !called {
		t.Fatal("handler was not called")
	}
	got, _ := repo.GetByName(context.Background(), job.Name)
	if repo.counts[got.ID] != 1 || repo.runs[got.ID].LastRunAt.IsZero() {
		t.Fatalf("job state = %#v", got)
	}
}

func TestManagerFailedHandlerStillUpdatesRunState(t *testing.T) {
	repo := newFakeCronRepo()
	manager := NewManager(repo)
	boom := errors.New("boom")
	if err := manager.RegisterHandler("test.handler", func(ctx context.Context, job Job) (globalevents.LogResult, error) {
		return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: boom.Error()}, boom
	}); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	job, err := manager.UpsertJob(context.Background(), UpsertJobRequest{Name: "test.job", Handler: "test.handler", Schedule: "0 3 * * *", Enabled: true})
	if err != nil {
		t.Fatalf("upsert job: %v", err)
	}

	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { <-manager.Stop().Done() }()
	manager.runJob(job.Name)
	got, _ := repo.GetByName(context.Background(), job.Name)
	if repo.counts[got.ID] != 1 {
		t.Fatalf("job state = %#v", got)
	}
}

func TestManagerUpsertStoresAndDisableClearsNextRunAt(t *testing.T) {
	repo := newFakeCronRepo()
	manager := NewManager(repo)
	job, err := manager.UpsertJob(context.Background(), UpsertJobRequest{Name: "test.job", Handler: "test.handler", Schedule: "0 3 * * *", Enabled: true})
	if err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	if job.NextRunAt == nil {
		t.Fatalf("next run was not stored: %#v", job)
	}
	if err := manager.DisableJob(context.Background(), job.Name); err != nil {
		t.Fatalf("disable job: %v", err)
	}
	if repo.jobs[job.Name].NextRunAt != nil {
		t.Fatalf("next run was not cleared: %#v", repo.jobs[job.Name])
	}
}

func TestManagerSchedulesNextRunState(t *testing.T) {
	future := time.Now().In(time.Local).Add(48 * time.Hour).Truncate(time.Minute)
	past := future.Add(-96 * time.Hour)
	onceMetadata := func(runAt time.Time) string {
		t.Helper()
		data, err := json.Marshal(Metadata{
			Kind: metadataKind, Version: 1,
			Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: runAt.Format(timeLayout)},
		})
		if err != nil {
			t.Fatalf("marshal once metadata: %v", err)
		}
		return string(data)
	}
	futureMetadata := onceMetadata(future)
	for _, tt := range []struct {
		name       string
		enabled    bool
		registered bool
		metadata   string
		initial    *time.Time
		want       *time.Time
		scheduled  bool
	}{
		{name: "recurring replaces old time", enabled: true, registered: true, initial: &past, scheduled: true},
		{name: "once without stored time", enabled: true, registered: true, metadata: futureMetadata, want: &future, scheduled: true},
		{name: "once correct time in another zone", enabled: true, registered: true, metadata: futureMetadata, initial: timePtr(future.In(time.FixedZone("other", -7*60*60))), want: &future, scheduled: true},
		{name: "once replaces old time", enabled: true, registered: true, metadata: futureMetadata, initial: &past, want: &future, scheduled: true},
		{name: "disabled clears stored time", registered: true, initial: &future},
		{name: "missing handler clears stored time", enabled: true, initial: &future},
		{name: "expired once clears stored time", enabled: true, registered: true, metadata: onceMetadata(past), initial: &future},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeCronRepo()
			manager := NewManager(repo)
			if tt.registered {
				if err := manager.RegisterHandler("test.handler", func(context.Context, Job) (globalevents.LogResult, error) {
					return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
				}); err != nil {
					t.Fatalf("register handler: %v", err)
				}
			}
			if err := manager.Start(context.Background()); err != nil {
				t.Fatalf("start manager: %v", err)
			}
			defer func() { <-manager.Stop().Done() }()
			job := Job{
				ID: "job", Name: "test.job", Handler: "test.handler", Schedule: "0 3 * * *",
				Enabled: tt.enabled, Metadata: tt.metadata, NextRunAt: tt.initial,
			}
			repo.jobs[job.Name] = &job
			before := time.Now()
			if err := manager.scheduleJob(context.Background(), job); err != nil {
				t.Fatalf("schedule job: %v", err)
			}
			stored, _ := repo.GetByName(context.Background(), job.Name)
			got := stored.NextRunAt
			if tt.want != nil {
				if got == nil || !got.Equal(*tt.want) {
					t.Fatalf("next run = %v, want %v", got, tt.want)
				}
			} else if tt.scheduled {
				if got == nil || !got.After(before) || got.Hour() != 3 || got.Minute() != 0 {
					t.Fatalf("recurring next run = %v", got)
				}
			} else if got != nil {
				t.Fatalf("unscheduled next run = %v, want nil", got)
			}
			manager.mu.Lock()
			_, scheduled := manager.entries[job.Name]
			manager.mu.Unlock()
			if scheduled != tt.scheduled {
				t.Fatalf("scheduled = %v, want %v", scheduled, tt.scheduled)
			}
		})
	}
}

func TestManagerRunJobDoesNotReenableJobDisabledByHandler(t *testing.T) {
	repo := newFakeCronRepo()
	manager := NewManager(repo)
	if err := manager.RegisterHandler("test.handler", func(ctx context.Context, job Job) (globalevents.LogResult, error) {
		err := manager.DisableJob(ctx, job.Name)
		if err != nil {
			return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
		}
		return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "job disabled"}, nil
	}); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	job, err := manager.UpsertJob(context.Background(), UpsertJobRequest{Name: "test.job", Handler: "test.handler", Schedule: "0 3 * * *", Enabled: true})
	if err != nil {
		t.Fatalf("upsert job: %v", err)
	}

	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { <-manager.Stop().Done() }()
	manager.runJob(job.Name)
	got, _ := repo.GetByName(context.Background(), job.Name)
	if got.Enabled {
		t.Fatalf("job was re-enabled: %#v", got)
	}
	if repo.counts[got.ID] != 1 || repo.runs[got.ID].LastRunAt.IsZero() {
		t.Fatalf("job state was not updated: %#v", got)
	}
}

func TestComputeNextRunAtUsesUserOnceRunAt(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 3, 0, 0, time.Local)
	meta := Metadata{Kind: metadataKind, Version: 1, Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	nextRun, err := computeNextRunAt("4 3 2 1 *", string(data), true, now)
	if err != nil {
		t.Fatalf("compute next run: %v", err)
	}
	want := time.Date(2026, 1, 2, 3, 4, 0, 0, time.Local)
	if nextRun == nil || !nextRun.Equal(want) {
		t.Fatalf("next run = %v, want %v", nextRun, want)
	}
	nextRun, err = computeNextRunAt("4 3 2 1 *", string(data), true, want)
	if err != nil {
		t.Fatalf("compute expired next run: %v", err)
	}
	if nextRun != nil {
		t.Fatalf("expired once next run = %v, want nil", nextRun)
	}
}

func TestManagerSkipsUnregisteredHandler(t *testing.T) {
	repo := newFakeCronRepo()
	manager := NewManager(repo)
	job, err := manager.UpsertJob(context.Background(), UpsertJobRequest{Name: "test.job", Handler: "missing.handler", Schedule: "0 3 * * *", Enabled: true})
	if err != nil {
		t.Fatalf("upsert job: %v", err)
	}

	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { <-manager.Stop().Done() }()
	manager.runJob(job.Name)
	got, _ := repo.GetByName(context.Background(), job.Name)
	if repo.counts[got.ID] != 0 || !repo.runs[got.ID].LastRunAt.IsZero() {
		t.Fatalf("job should not run: %#v", got)
	}
}

func TestManagerStartSchedulesEnabledJobs(t *testing.T) {
	repo := newFakeCronRepo()
	manager := NewManager(repo)
	if err := manager.RegisterHandler("test.handler", func(ctx context.Context, job Job) (globalevents.LogResult, error) {
		return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "finished"}, nil
	}); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	if _, err := manager.UpsertJob(context.Background(), UpsertJobRequest{Name: "test.job", Handler: "test.handler", Schedule: "0 3 * * *", Enabled: true}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	stopCtx := manager.Stop()
	select {
	case <-stopCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("cron manager did not stop")
	}
}

func TestManagerStopBeforeStartDoesNotBlock(t *testing.T) {
	manager := NewManager(nil)
	stopCtx := manager.Stop()
	select {
	case <-stopCtx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("cron manager stop before start did not finish")
	}
}
