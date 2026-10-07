package cron

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/storage"
)

func awaitCronDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cron did not finish")
	}
}

func TestManagerCancellationAndRepeatedStopWaitForHandlerExit(t *testing.T) {
	repo := newFakeCronRepo()
	var logs bytes.Buffer
	manager := NewManager(repo)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	if err := manager.RegisterHandler("wait", func(ctx context.Context, _ storage.CronJob) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	job, err := manager.UpsertJob(context.Background(), UpsertJobRequest{Name: "wait", Handler: "wait", Schedule: "0 0 1 1 *", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	go manager.runJob(job.Name)
	awaitCronDone(t, started)
	cancel()
	awaitCronDone(t, canceled)
	first, second := manager.Stop(), manager.Stop()
	if first != second {
		t.Fatal("repeated stop returned different completion")
	}
	select {
	case <-second.Done():
		t.Fatal("stop completed while handler was alive")
	default:
	}
	close(release)
	awaitCronDone(t, first.Done())
	awaitCronDone(t, second.Done())
	state := repo.jobs[job.Name]
	if state.RunCount != 1 || state.LastError != "" || state.NextRunAt != nil {
		t.Fatalf("canceled state=%+v", state)
	}
	if strings.Contains(logs.String(), "job failed") {
		t.Fatalf("normal shutdown reported failure: %s", logs.String())
	}
	if err := manager.Start(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("late start reopened stopped manager: %v", err)
	}
}

type lifecycleCronRepo struct {
	storage.CronJobRepository
	list   func(context.Context) ([]storage.CronJob, error)
	finish func(context.Context, string, storage.CronJobRunState) error
}

func (r lifecycleCronRepo) ListEnabled(ctx context.Context) ([]storage.CronJob, error) {
	if r.list != nil {
		return r.list(ctx)
	}
	return r.CronJobRepository.ListEnabled(ctx)
}

func (r lifecycleCronRepo) UpdateRunState(ctx context.Context, id string, state storage.CronJobRunState) error {
	if r.finish != nil {
		return r.finish(ctx, id, state)
	}
	return r.CronJobRepository.UpdateRunState(ctx, id, state)
}

func TestManagerStopWaitsForStartupAndPreventsLateScheduling(t *testing.T) {
	base := newFakeCronRepo()
	started, release := make(chan struct{}), make(chan struct{})
	manager := NewManager(lifecycleCronRepo{CronJobRepository: base, list: func(context.Context) ([]storage.CronJob, error) {
		close(started)
		<-release
		return []storage.CronJob{{Name: "late", Handler: "late", Enabled: true, Schedule: "@every 1s"}}, nil
	}})
	if err := manager.RegisterHandler("late", func(context.Context, storage.CronJob) error { t.Error("late handler ran"); return nil }); err != nil {
		t.Fatal(err)
	}
	startErr := make(chan error, 1)
	go func() { startErr <- manager.Start(context.Background()) }()
	awaitCronDone(t, started)
	stopped := manager.Stop()
	select {
	case <-stopped.Done():
		t.Fatal("stop ignored active startup")
	default:
	}
	close(release)
	awaitCronDone(t, stopped.Done())
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start=%v", err)
	}
	if len(manager.entries) != 0 || manager.started {
		t.Fatal("startup reopened scheduling")
	}
}

func TestManagerStartupFailureAndStopBeforeStart(t *testing.T) {
	for _, stopFirst := range []bool{false, true} {
		t.Run(map[bool]string{true: "stop first", false: "load failure"}[stopFirst], func(t *testing.T) {
			failure := errors.New("load failed")
			loads := 0
			manager := NewManager(lifecycleCronRepo{CronJobRepository: newFakeCronRepo(), list: func(context.Context) ([]storage.CronJob, error) { loads++; return nil, failure }})
			if stopFirst {
				awaitCronDone(t, manager.Stop().Done())
			}
			err := manager.Start(context.Background())
			if stopFirst {
				if !errors.Is(err, context.Canceled) || loads != 0 {
					t.Fatalf("late Start=%v loads=%d", err, loads)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("Start=%v", err)
			}
			first := manager.Stop()
			awaitCronDone(t, first.Done())
			if manager.Stop() != first {
				t.Fatal("completion changed")
			}
		})
	}
}

func TestManagerStopWaitsForRunBookkeeping(t *testing.T) {
	base := newFakeCronRepo()
	finishing, release := make(chan struct{}), make(chan struct{})
	manager := NewManager(lifecycleCronRepo{CronJobRepository: base, finish: func(ctx context.Context, id string, state storage.CronJobRunState) error {
		close(finishing)
		<-release
		return base.UpdateRunState(ctx, id, state)
	}})
	if err := manager.RegisterHandler("test", func(context.Context, storage.CronJob) error { return nil }); err != nil {
		t.Fatal(err)
	}
	job, err := manager.UpsertJob(context.Background(), UpsertJobRequest{Name: "test", Handler: "test", Schedule: "0 0 1 1 *", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	go manager.runJob(job.Name)
	awaitCronDone(t, finishing)
	stop := manager.Stop()
	select {
	case <-stop.Done():
		t.Fatal("stop released storage while bookkeeping was alive")
	default:
	}
	close(release)
	awaitCronDone(t, stop.Done())
}
