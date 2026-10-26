package cron

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/events"
)

type refreshRepo struct {
	JobRepository
	get  func(context.Context, string) (*Job, error)
	list func(context.Context) ([]Job, error)
}

func (r refreshRepo) GetByName(ctx context.Context, name string) (*Job, error) {
	if r.get != nil {
		return r.get(ctx, name)
	}
	return r.JobRepository.GetByName(ctx, name)
}

func (r refreshRepo) ListEnabled(ctx context.Context) ([]Job, error) {
	if r.list != nil {
		return r.list(ctx)
	}
	return r.JobRepository.ListEnabled(ctx)
}

func registerRefreshHandler(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.RegisterHandler("handler", func(context.Context, Job) (events.LogResult, error) {
		return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func seedRefreshJob(t *testing.T, repo *fakeCronRepo, name string) *Job {
	t.Helper()
	job, err := repo.Upsert(t.Context(), UpsertJobRequest{Name: name, Handler: "handler", Schedule: "0 3 * * *", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func awaitRefreshCondition(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !check() {
		select {
		case <-deadline.C:
			t.Fatal("refresh did not converge")
		case <-tick.C:
		}
	}
}

func scheduledJob(m *Manager, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.entries[name]
	return ok
}

func TestRefreshCachesBeforeStartAndOverridesInitialSnapshot(t *testing.T) {
	base := newFakeCronRepo()
	seedRefreshJob(t, base, "job")
	listed, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	m := NewManager(refreshRepo{JobRepository: base,
		list: func(ctx context.Context) ([]Job, error) {
			rows, err := base.ListEnabled(ctx)
			close(listed)
			<-release
			return rows, err
		},
		get: func(ctx context.Context, name string) (*Job, error) { reads.Add(1); return base.GetByName(ctx, name) },
	})
	registerRefreshHandler(t, m)
	for range 3 {
		if err := m.RefreshJob("job"); err != nil {
			t.Fatal(err)
		}
	}
	if reads.Load() != 0 {
		t.Fatal("notification performed storage I/O")
	}
	started := make(chan error, 1)
	go func() { started <- m.Start(t.Context()) }()
	awaitCronDone(t, listed)
	if err := base.DisableByName(t.Context(), "job"); err != nil {
		t.Fatal(err)
	}
	if err := m.RefreshJob("job"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	defer func() { awaitCronDone(t, m.Stop().Done()) }()
	if reads.Load() != 1 || scheduledJob(m, "job") {
		t.Fatalf("reads=%d scheduled=%v", reads.Load(), scheduledJob(m, "job"))
	}
	job, _ := base.GetByName(t.Context(), "job")
	if job.Enabled || job.NextRunAt != nil {
		t.Fatalf("stale initial snapshot survived: %#v", job)
	}
}

func TestRefreshReadsChangesArrivingDuringReadAgain(t *testing.T) {
	base := newFakeCronRepo()
	seedRefreshJob(t, base, "job")
	reading, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	m := NewManager(refreshRepo{JobRepository: base, get: func(ctx context.Context, name string) (*Job, error) {
		job, err := base.GetByName(ctx, name)
		if reads.Add(1) == 1 {
			close(reading)
			<-release
		}
		return job, err
	}})
	registerRefreshHandler(t, m)
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { awaitCronDone(t, m.Stop().Done()) }()
	if err := m.RefreshJob("job"); err != nil {
		t.Fatal(err)
	}
	awaitCronDone(t, reading)
	if err := base.DisableByName(t.Context(), "job"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := m.RefreshJob("job"); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	awaitRefreshCondition(t, func() bool {
		job, _ := base.GetByName(t.Context(), "job")
		return reads.Load() == 2 && !scheduledJob(m, "job") && job.NextRunAt == nil
	})
	job, _ := base.GetByName(t.Context(), "job")
	if job.NextRunAt != nil {
		t.Fatalf("disabled next run=%v", job.NextRunAt)
	}
}

func TestRefreshReloadsModifiedDeletedAndRecreatedJobs(t *testing.T) {
	base := newFakeCronRepo()
	old := seedRefreshJob(t, base, "job")
	m := NewManager(base)
	registerRefreshHandler(t, m)
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { awaitCronDone(t, m.Stop().Done()) }()
	_, err := base.Upsert(t.Context(), UpsertJobRequest{Name: "job", Handler: "handler", Schedule: "0 4 * * *", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.RefreshJob("job")
	awaitRefreshCondition(t, func() bool {
		job, _ := base.GetByName(t.Context(), "job")
		return job.NextRunAt != nil && job.NextRunAt.Hour() == 4
	})
	if err := base.DeleteByName(t.Context(), "job"); err != nil {
		t.Fatal(err)
	}
	_ = m.RefreshJob("job")
	awaitRefreshCondition(t, func() bool { return !scheduledJob(m, "job") })
	job := seedRefreshJob(t, base, "job")
	if old.ID == job.ID {
		t.Fatal("fixture did not recreate identity")
	}
	_ = m.RefreshJob("job")
	awaitRefreshCondition(t, func() bool { return scheduledJob(m, "job") })
}

func TestRefreshStopWaitsForActiveReadAndRejectsLateWork(t *testing.T) {
	base := newFakeCronRepo()
	seedRefreshJob(t, base, "job")
	reading, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	m := NewManager(refreshRepo{JobRepository: base, get: func(ctx context.Context, name string) (*Job, error) {
		reads.Add(1)
		close(reading)
		<-release
		return base.GetByName(ctx, name)
	}})
	registerRefreshHandler(t, m)
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = m.RefreshJob("job")
	awaitCronDone(t, reading)
	stopped := m.Stop()
	select {
	case <-stopped.Done():
		t.Fatal("stop ignored active refresh")
	default:
	}
	_ = m.RefreshJob("late")
	close(release)
	awaitCronDone(t, stopped.Done())
	if reads.Load() != 1 {
		t.Fatalf("late reads=%d", reads.Load())
	}
}

func TestRefreshIsolatesBadDefinitionAndReadFailure(t *testing.T) {
	base := newFakeCronRepo()
	seedRefreshJob(t, base, "good")
	bad := seedRefreshJob(t, base, "bad")
	base.jobs[bad.Name].Metadata = "{broken"
	failure := errors.New("read failed")
	var fail atomic.Bool
	fail.Store(true)
	m := NewManager(refreshRepo{JobRepository: base, get: func(ctx context.Context, name string) (*Job, error) {
		if fail.Swap(false) {
			return nil, failure
		}
		return base.GetByName(ctx, name)
	}})
	registerRefreshHandler(t, m)
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { awaitCronDone(t, m.Stop().Done()) }()
	if !scheduledJob(m, "good") || scheduledJob(m, "bad") {
		t.Fatal("bad row affected healthy scheduling")
	}
	_ = m.RefreshJob("bad")
	awaitRefreshCondition(t, func() bool { return !fail.Load() })
	base.mu.Lock()
	base.jobs["bad"].Metadata = ""
	base.mu.Unlock()
	_ = m.RefreshJob("bad")
	awaitRefreshCondition(t, func() bool { return scheduledJob(m, "bad") })
}

func TestFinishingOldRunDoesNotCountIntoRecreatedJob(t *testing.T) {
	base := newFakeCronRepo()
	old := seedRefreshJob(t, base, "job")
	started, release := make(chan struct{}), make(chan struct{})
	m := NewManager(base)
	if err := m.RegisterHandler("handler", func(context.Context, Job) (events.LogResult, error) {
		close(started)
		<-release
		return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() { m.runJob("job"); close(finished) }()
	awaitCronDone(t, started)
	if err := base.DeleteByName(t.Context(), "job"); err != nil {
		t.Fatal(err)
	}
	current := seedRefreshJob(t, base, "job")
	close(release)
	awaitCronDone(t, finished)
	awaitCronDone(t, m.Stop().Done())
	if base.counts[current.ID] != 0 || old.ID == current.ID {
		t.Fatalf("new job received old run: %#v", base.counts)
	}
}
