package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/logging"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type observedLogManager struct {
	*logging.Manager
	begun chan struct{}
	once  sync.Once
}

func (m *observedLogManager) BeginClose() {
	m.Manager.BeginClose()
	m.once.Do(func() { close(m.begun) })
}

func TestRunnerStopsGlobalLogAdmissionBeforeWaitingForPlatform(t *testing.T) {
	var calls []string
	runner := newTestRunner(t, &calls, RunModeFull, "")
	manager, err := logging.NewManager("info", filepath.Join(t.TempDir(), "db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	logs := &observedLogManager{Manager: manager, begun: make(chan struct{})}
	build := runner.deps.Foundation
	var foundationDeadline, runtimeDeadline time.Time
	runner.deps.Foundation = foundationFactoryFunc(func(ctx context.Context, req FoundationRequest) (*FoundationComponents, error) {
		result, err := build.Build(ctx, req)
		result.Logs = logs
		result.Lifecycle = lifecycleFunc(func(ctx context.Context) error {
			foundationDeadline, _ = ctx.Deadline()
			return (&foundationLifecycle{logs: logs}).Close(ctx)
		})
		return result, err
	})
	runtimeBuild := runner.deps.Runtime
	runner.deps.Runtime = runtimeFactoryFunc(func(ctx context.Context, req RuntimeRequest) (*RuntimeComponents, error) {
		result, err := runtimeBuild.Build(ctx, req)
		result.Lifecycle = lifecycleFunc(func(ctx context.Context) error { runtimeDeadline, _ = ctx.Deadline(); return nil })
		return result, err
	})
	runner.deps.Executor = executorFunc(func(ctx context.Context, req PlatformRunRequest) error {
		if err := events.EmitLog(ctx, events.LogRecord{Category: events.LogAudit, Summary: "platform started"}); err != nil {
			return err
		}
		req.Stop()
		select {
		case <-logs.begun:
		case <-time.After(time.Second):
			return errors.New("log admission remained open while waiting for platform")
		}
		// Publish through the business entry point. The drained file below must
		// contain only the record admitted before BeginClose.
		if err := events.EmitLog(context.Background(), events.LogRecord{Category: events.LogAudit, Summary: "after admission closed"}); err != nil {
			return err
		}
		return ctx.Err()
	})
	if err := runner.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if foundationDeadline.IsZero() || foundationDeadline != runtimeDeadline {
		t.Fatalf("separate shutdown budgets: %v %v", foundationDeadline, runtimeDeadline)
	}
	entries, err := (logging.Reader{Dir: manager.LogDir()}).Query(context.Background(), logging.LogQuery{Prefix: "audit"})
	if err != nil || len(entries) != 1 || entries[0].Message != "platform started" {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	next, err := logging.NewManager("info", filepath.Join(t.TempDir(), "db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	_ = next.Close(context.Background())
}

func TestRunnerClosesGlobalLogsFromPartialFoundation(t *testing.T) {
	var calls []string
	runner := newTestRunner(t, &calls, RunModeFull, "")
	manager, err := logging.NewManager("info", filepath.Join(t.TempDir(), "db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	logs := &observedLogManager{Manager: manager, begun: make(chan struct{})}
	failure := errors.New("storage initialization failed")
	runner.deps.Foundation = foundationFactoryFunc(func(context.Context, FoundationRequest) (*FoundationComponents, error) {
		return &FoundationComponents{Logs: logs, Lifecycle: &foundationLifecycle{logs: logs}}, failure
	})
	if err := runner.Run(context.Background(), Options{}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	select {
	case <-logs.begun:
	default:
		t.Fatal("partial foundation missed BeginClose")
	}
	next, err := logging.NewManager("info", filepath.Join(t.TempDir(), "db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	_ = next.Close(context.Background())
}

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
