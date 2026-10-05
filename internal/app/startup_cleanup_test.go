package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestRunnerKeepsDependenciesWhileRuntimeWorkerIsAlive(t *testing.T) {
	events := []string{}
	runner := newTestRunner(t, &events, RunModeFull, "")
	runner.shutdownTimeout = 20 * time.Millisecond
	done := make(chan struct{})
	cancelCalled := false
	lifecycle := &runtimeLifecycle{cancel: func() { cancelCalled = true }, skillDone: done}
	failure := errors.New("command registration failed")
	runner.deps.Runtime = runtimeFactoryFunc(func(context.Context, RuntimeRequest) (*RuntimeComponents, error) {
		return &RuntimeComponents{Lifecycle: lifecycle}, failure
	})
	if err := runner.Run(context.Background(), Options{}); !errors.Is(err, failure) {
		t.Fatalf("lost startup error: %v", err)
	}
	if !cancelCalled || lifecycle.stopped() {
		t.Fatal("worker cancellation/completion confused")
	}
	for _, event := range events {
		if event == "foundation-close" || event == "marker-close" {
			t.Fatalf("released live worker dependency: %s", event)
		}
	}
	close(done)
	if !lifecycle.stopped() {
		t.Fatal("completed worker still active")
	}
}

func TestFoundationDeadlineKeepsCronDependenciesOpen(t *testing.T) {
	store, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lifecycle := &foundationLifecycle{store: store, cronScheduled: true, cronStartupDone: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lifecycle.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close = %v", err)
	}
	if _, err := store.Sessions().Get(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("closed live cron storage: %v", err)
	}
}

func TestRunnerPlatformStopConsumesShutdownBudget(t *testing.T) {
	events := []string{}
	runner := newTestRunner(t, &events, RunModeFull, "")
	runner.shutdownTimeout = 20 * time.Millisecond
	release, exited := make(chan struct{}), make(chan struct{})
	runner.deps.Executor = executorFunc(func(ctx context.Context, req PlatformRunRequest) error {
		defer close(exited)
		req.Stop()
		<-ctx.Done()
		<-release // Simulate an adapter that cannot finish within the budget.
		return ctx.Err()
	})
	started := time.Now()
	if err := runner.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("platform shutdown exceeded budget")
	}
	for _, event := range events {
		if event == "runtime-close" || event == "foundation-close" || event == "marker-close" {
			t.Fatalf("released a live platform's dependency: %s", event)
		}
	}
	close(release)
	<-exited
	for _, event := range events {
		if event == "runtime-close" || event == "foundation-close" {
			t.Fatal("background cleanup resumed")
		}
	}
}

func TestRunnerCleansPartialStageWithSharedDeadline(t *testing.T) {
	for _, stage := range []string{"foundation", "runtime"} {
		t.Run(stage, func(t *testing.T) {
			events := []string{}
			runner := newTestRunner(t, &events, RunModeFull, "")
			failure := errors.New("partial startup")
			var deadlines []time.Time
			closePartial := lifecycleFunc(func(ctx context.Context) error {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Error("partial cleanup has no deadline")
				}
				deadlines = append(deadlines, deadline)
				return nil
			})
			if stage == "foundation" {
				runner.deps.Foundation = foundationFactoryFunc(func(context.Context, FoundationRequest) (*FoundationComponents, error) {
					return &FoundationComponents{Lifecycle: closePartial}, failure
				})
			} else {
				build := runner.deps.Foundation
				runner.deps.Foundation = foundationFactoryFunc(func(ctx context.Context, req FoundationRequest) (*FoundationComponents, error) {
					result, err := build.Build(ctx, req)
					result.Lifecycle = closePartial
					return result, err
				})
				runner.deps.Runtime = runtimeFactoryFunc(func(context.Context, RuntimeRequest) (*RuntimeComponents, error) {
					return &RuntimeComponents{Lifecycle: closePartial}, failure
				})
			}
			if err := runner.Run(context.Background(), Options{}); !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			want := 1
			if stage == "runtime" {
				want = 2
			}
			if len(deadlines) != want {
				t.Fatalf("cleanup calls = %d, want %d", len(deadlines), want)
			}
			for _, deadline := range deadlines {
				if deadline != deadlines[0] {
					t.Fatalf("cleanup received separate budgets: %v", deadlines)
				}
			}
		})
	}
}
