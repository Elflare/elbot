package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	elcron "elbot/internal/cron"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestReviewCronStoppedBeforeRuntimeDependencies(t *testing.T) {
	store, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := elcron.NewManager(store.CronJobs())
	started := make(chan struct{}, 1)
	runtimeClosing := make(chan struct{})
	observed := make(chan bool, 1)
	if err := manager.RegisterHandler("review", func(ctx context.Context, job storage.CronJob) error {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			observed <- false
		case <-runtimeClosing:
			observed <- ctx.Err() == nil
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = manager.UpsertJob(context.Background(), elcron.UpsertJobRequest{Name: "review", Handler: "review", Schedule: "@every 1s", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { <-manager.Stop().Done() }()
	events := []string{}
	runner := newTestRunner(t, &events, RunModeFull, "")
	foundation := runner.deps.Foundation
	runner.deps.Foundation = foundationFactoryFunc(func(ctx context.Context, req FoundationRequest) (*FoundationComponents, error) {
		result, err := foundation.Build(ctx, req)
		lifecycle := &foundationLifecycle{cronManager: manager}
		result.Lifecycle = lifecycle
		result.StopCron = lifecycle.StopCron
		return result, err
	})
	runtime := runner.deps.Runtime
	runner.deps.Runtime = runtimeFactoryFunc(func(ctx context.Context, req RuntimeRequest) (*RuntimeComponents, error) {
		result, err := runtime.Build(ctx, req)
		result.Lifecycle = lifecycleFunc(func(context.Context) error { close(runtimeClosing); return nil })
		return result, err
	})
	runner.deps.Executor = executorFunc(func(ctx context.Context, req PlatformRunRequest) error {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Error("job never started")
		}
		req.Stop()
		return nil
	})
	if err := runner.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	select {
	case alive := <-observed:
		if alive {
			t.Fatal("runtime dependencies closed while cron handler remains active with uncanceled context")
		}
	default:
		t.Fatal("no cron lifecycle observation")
	}
}

func TestRunnerKeepsDependenciesUntilCronActuallyExits(t *testing.T) {
	store, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := elcron.NewManager(store.CronJobs())
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
	if _, err := manager.UpsertJob(context.Background(), elcron.UpsertJobRequest{Name: "wait", Handler: "wait", Schedule: "@every 1s", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { <-manager.Stop().Done() }()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	events := []string{}
	runner := newTestRunner(t, &events, RunModeFull, "")
	runner.shutdownTimeout = 20 * time.Millisecond
	build := runner.deps.Foundation
	lifecycle := &foundationLifecycle{store: store, cronManager: manager}
	runner.deps.Foundation = foundationFactoryFunc(func(ctx context.Context, req FoundationRequest) (*FoundationComponents, error) {
		result, err := build.Build(ctx, req)
		result.StopCron = lifecycle.StopCron
		result.Lifecycle = lifecycle
		return result, err
	})
	runner.deps.Executor = executorFunc(func(ctx context.Context, req PlatformRunRequest) error {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Error("handler never started")
		}
		req.Stop()
		return nil
	})
	if err := runner.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("shutdown did not cancel handler")
	}
	for _, e := range events {
		if e == "runtime-close" || e == "marker-close" {
			t.Fatalf("released cron dependency: %s", e)
		}
	}
	if _, err := store.Sessions().Get(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("closed cron storage: %v", err)
	}
	// A timed-out caller must still wait on the original completion.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lifecycle.StopCron(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("second stop ignored live handler: %v", err)
	}
	unblock()
	<-manager.Stop().Done()
	for _, e := range events {
		if e == "runtime-close" || e == "marker-close" {
			t.Fatalf("background cleanup resumed: %s", e)
		}
	}
	if _, err := store.Sessions().Get(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("background cleanup closed storage: %v", err)
	}
}

func TestFoundationStopWaitsForStartupAndRejectsLateStart(t *testing.T) {
	store, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	startup, canceled := make(chan struct{}), make(chan struct{})
	lifecycle := &foundationLifecycle{cronManager: elcron.NewManager(store.CronJobs()), cronScheduled: true, cronStartupDone: startup, cronCancel: func() {
		select {
		case <-canceled:
		default:
			close(canceled)
		}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := lifecycle.StopCron(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StopCron=%v", err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("startup was not canceled")
	}
	close(startup)
	if err := lifecycle.StopCron(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A late AfterStart callback cannot re-open a stopped foundation.
	lifecycle.cronScheduled = false
	lifecycle.startCron(context.Background(), &elcron.Service{})
	if lifecycle.cronScheduled {
		t.Fatal("late startup reopened cron")
	}
}

func TestRunnerCronSharesDependencyShutdownDeadline(t *testing.T) {
	events := []string{}
	runner := newTestRunner(t, &events, RunModeFull, "")
	var cronDeadline time.Time
	build := runner.deps.Foundation
	runner.deps.Foundation = foundationFactoryFunc(func(ctx context.Context, req FoundationRequest) (*FoundationComponents, error) {
		result, err := build.Build(ctx, req)
		result.StopCron = func(ctx context.Context) error { cronDeadline, _ = ctx.Deadline(); return nil }
		result.Lifecycle = lifecycleFunc(func(ctx context.Context) error {
			deadline, _ := ctx.Deadline()
			if deadline != cronDeadline {
				t.Error("foundation received a new budget")
			}
			return nil
		})
		return result, err
	})
	runtime := runner.deps.Runtime
	runner.deps.Runtime = runtimeFactoryFunc(func(ctx context.Context, req RuntimeRequest) (*RuntimeComponents, error) {
		result, err := runtime.Build(ctx, req)
		result.Lifecycle = lifecycleFunc(func(ctx context.Context) error {
			deadline, _ := ctx.Deadline()
			if cronDeadline.IsZero() || deadline != cronDeadline {
				t.Error("runtime closed before cron or received a new budget")
			}
			return nil
		})
		return result, err
	})
	if err := runner.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
}
