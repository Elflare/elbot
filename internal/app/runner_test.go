package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	elcron "elbot/internal/cron"
)

type environmentStub struct {
	events    *[]string
	mode      RunMode
	profiler  StartupProfiler
	markerErr error
}

func (s environmentStub) ResolveMode(RunMode) (RunMode, error) {
	*s.events = append(*s.events, "resolve")
	return s.mode, nil
}

func (s environmentStub) ClaimServiceMarker(RunMode) (io.Closer, error) {
	*s.events = append(*s.events, "marker")
	return closeFunc(func() error {
		*s.events = append(*s.events, "marker-close")
		return s.markerErr
	}), nil
}

func (s environmentStub) NewStartupProfiler(time.Time) StartupProfiler {
	*s.events = append(*s.events, "profiler")
	return s.profiler
}

type closeFunc func() error

func (f closeFunc) Close() error {
	return f()
}

type lifecycleFunc func(context.Context) error

func (f lifecycleFunc) Close(ctx context.Context) error { return f(ctx) }

type profilerStub struct {
	events *[]string
}

func (p profilerStub) SetEnabled(bool) {}
func (p profilerStub) Mark(string)     {}
func (p profilerStub) Flush() time.Duration {
	*p.events = append(*p.events, "flush")
	return time.Second
}

type foundationFactoryFunc func(context.Context, FoundationRequest) (*FoundationComponents, error)

func (f foundationFactoryFunc) Build(ctx context.Context, req FoundationRequest) (*FoundationComponents, error) {
	return f(ctx, req)
}

type modelFactoryFunc func(ModelRequest) (ModelClients, error)

func (f modelFactoryFunc) Build(req ModelRequest) (ModelClients, error) { return f(req) }

type platformFactoryFunc func(PlatformRequest) (PlatformComponents, error)

func (f platformFactoryFunc) Build(req PlatformRequest) (PlatformComponents, error) {
	return f(req)
}

type runtimeFactoryFunc func(context.Context, RuntimeRequest) (*RuntimeComponents, error)

func (f runtimeFactoryFunc) Build(ctx context.Context, req RuntimeRequest) (*RuntimeComponents, error) {
	return f(ctx, req)
}

type integrationFactoryFunc func(context.Context, IntegrationRequest) (PlatformComponents, error)

func (f integrationFactoryFunc) Attach(ctx context.Context, req IntegrationRequest) (PlatformComponents, error) {
	return f(ctx, req)
}

type executorFunc func(context.Context, PlatformRunRequest) error

func (f executorFunc) Run(ctx context.Context, req PlatformRunRequest) error { return f(ctx, req) }

type handlerStub struct{}

func (handlerStub) HandleMessage(context.Context, string) error { return nil }

func TestRunnerRunOrdersStagesAndCleanup(t *testing.T) {
	events := []string{}
	runner := newTestRunner(t, &events, RunModeFull, "")

	if err := runner.Run(context.Background(), Options{Mode: RunModeFull}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{
		"resolve", "marker", "profiler", "foundation", "models", "platforms", "runtime", "integrations",
		"flush", "executor", "cron-start", "runtime-close", "foundation-close", "marker-close",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestRunnerRunSkipsCronInCLIOnlyMode(t *testing.T) {
	events := []string{}
	runner := newTestRunner(t, &events, RunModeCLIOnly, "")

	if err := runner.Run(context.Background(), Options{Mode: RunModeCLIOnly}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, event := range events {
		if event == "cron-start" {
			t.Fatal("CLI-only run started cron")
		}
	}
}

func TestRunnerRunCleansCompletedStagesAfterFailure(t *testing.T) {
	for _, failAt := range []string{"foundation", "models", "platforms", "runtime", "integrations", "executor"} {
		t.Run(failAt, func(t *testing.T) {
			events := []string{}
			runner := newTestRunner(t, &events, RunModeFull, failAt)

			err := runner.Run(context.Background(), Options{Mode: RunModeFull})
			if err == nil || err.Error() != "fail at "+failAt {
				t.Fatalf("Run() error = %v", err)
			}

			wantTail := []string{"marker-close"}
			if failAt == "models" || failAt == "platforms" || failAt == "runtime" {
				wantTail = []string{"foundation-close", "marker-close"}
			}
			if failAt == "integrations" || failAt == "executor" {
				wantTail = []string{"runtime-close", "foundation-close", "marker-close"}
			}
			if len(events) < len(wantTail) || !reflect.DeepEqual(events[len(events)-len(wantTail):], wantTail) {
				t.Fatalf("events tail = %#v, want %#v; all events = %#v", events, wantTail, events)
			}
		})
	}
}

func TestRunnerRunStopsBeforeEnvironmentWhenContextCanceled(t *testing.T) {
	events := []string{}
	runner := newTestRunner(t, &events, RunModeFull, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runner.Run(ctx, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none", events)
	}
}

func TestRunnerRunJoinsExecutionAndCleanupErrors(t *testing.T) {
	events := []string{}
	runErr := errors.New("executor failed")
	runtimeCloseErr := errors.New("runtime close failed")
	foundationCloseErr := errors.New("foundation close failed")
	markerCloseErr := errors.New("marker close failed")
	runner := newLifecycleErrorRunner(t, &events, runErr, runtimeCloseErr, foundationCloseErr, markerCloseErr)

	err := runner.Run(context.Background(), Options{Mode: RunModeFull})
	for _, want := range []error{runErr, runtimeCloseErr, foundationCloseErr, markerCloseErr} {
		if !errors.Is(err, want) {
			t.Fatalf("Run() error = %v, want errors.Is(%v)", err, want)
		}
	}
	wantTail := []string{"runtime-close", "foundation-close", "marker-close"}
	if !reflect.DeepEqual(events[len(events)-len(wantTail):], wantTail) {
		t.Fatalf("events = %#v, want tail %#v", events, wantTail)
	}
}

func TestRunnerRunBoundsCleanupWithSharedTimeout(t *testing.T) {
	events := []string{}
	runner := newLifecycleErrorRunner(t, &events, nil, nil, nil, nil)
	runner.shutdownTimeout = 20 * time.Millisecond
	runner.deps.Runtime = runtimeFactoryFunc(func(context.Context, RuntimeRequest) (*RuntimeComponents, error) {
		return &RuntimeComponents{
			Handler: handlerStub{},
			Lifecycle: lifecycleFunc(func(ctx context.Context) error {
				events = append(events, "runtime-close")
				<-ctx.Done()
				return ctx.Err()
			}),
		}, nil
	})

	started := time.Now()
	err := runner.Run(context.Background(), Options{Mode: RunModeFull})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Run() cleanup took %v", elapsed)
	}
	wantTail := []string{"runtime-close", "foundation-close", "marker-close"}
	if !reflect.DeepEqual(events[len(events)-len(wantTail):], wantTail) {
		t.Fatalf("events = %#v, want tail %#v", events, wantTail)
	}
}

func TestNewRunnerRejectsMissingDependencyGroup(t *testing.T) {
	deps := DefaultDependencies()
	deps.Models = nil
	if _, err := NewRunner(deps); err == nil {
		t.Fatal("NewRunner() error = nil")
	}
}

func newTestRunner(t *testing.T, events *[]string, mode RunMode, failAt string) *Runner {
	t.Helper()
	fail := func(stage string) error {
		if failAt == stage {
			return errors.New("fail at " + stage)
		}
		return nil
	}
	profiler := profilerStub{events: events}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	deps := Dependencies{
		Environment: environmentStub{events: events, mode: mode, profiler: profiler},
		Foundation: foundationFactoryFunc(func(context.Context, FoundationRequest) (*FoundationComponents, error) {
			*events = append(*events, "foundation")
			if err := fail("foundation"); err != nil {
				return nil, err
			}
			return &FoundationComponents{
				Logger: logger,
				StartCron: func(context.Context, *elcron.Service) {
					*events = append(*events, "cron-start")
				},
				Lifecycle: lifecycleFunc(func(context.Context) error {
					*events = append(*events, "foundation-close")
					return nil
				}),
			}, nil
		}),
		Models: modelFactoryFunc(func(ModelRequest) (ModelClients, error) {
			*events = append(*events, "models")
			return ModelClients{}, fail("models")
		}),
		Platforms: platformFactoryFunc(func(PlatformRequest) (PlatformComponents, error) {
			*events = append(*events, "platforms")
			return PlatformComponents{}, fail("platforms")
		}),
		Runtime: runtimeFactoryFunc(func(context.Context, RuntimeRequest) (*RuntimeComponents, error) {
			*events = append(*events, "runtime")
			if err := fail("runtime"); err != nil {
				return nil, err
			}
			return &RuntimeComponents{
				Handler: handlerStub{},
				Lifecycle: lifecycleFunc(func(context.Context) error {
					*events = append(*events, "runtime-close")
					return nil
				}),
			}, nil
		}),
		Integrations: integrationFactoryFunc(func(_ context.Context, req IntegrationRequest) (PlatformComponents, error) {
			*events = append(*events, "integrations")
			return req.Platforms, fail("integrations")
		}),
		Executor: executorFunc(func(_ context.Context, req PlatformRunRequest) error {
			*events = append(*events, "executor")
			if req.AfterStart != nil {
				req.AfterStart(context.Background())
			}
			return fail("executor")
		}),
	}
	runner, err := NewRunner(deps)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	return runner
}

func newLifecycleErrorRunner(t *testing.T, events *[]string, runErr, runtimeCloseErr, foundationCloseErr, markerCloseErr error) *Runner {
	t.Helper()
	profiler := profilerStub{events: events}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := Dependencies{
		Environment: environmentStub{events: events, mode: RunModeFull, profiler: profiler, markerErr: markerCloseErr},
		Foundation: foundationFactoryFunc(func(context.Context, FoundationRequest) (*FoundationComponents, error) {
			*events = append(*events, "foundation")
			return &FoundationComponents{
				Logger: logger,
				Lifecycle: lifecycleFunc(func(context.Context) error {
					*events = append(*events, "foundation-close")
					return foundationCloseErr
				}),
			}, nil
		}),
		Models: modelFactoryFunc(func(ModelRequest) (ModelClients, error) {
			*events = append(*events, "models")
			return ModelClients{}, nil
		}),
		Platforms: platformFactoryFunc(func(PlatformRequest) (PlatformComponents, error) {
			*events = append(*events, "platforms")
			return PlatformComponents{}, nil
		}),
		Runtime: runtimeFactoryFunc(func(context.Context, RuntimeRequest) (*RuntimeComponents, error) {
			*events = append(*events, "runtime")
			return &RuntimeComponents{
				Handler: handlerStub{},
				Lifecycle: lifecycleFunc(func(context.Context) error {
					*events = append(*events, "runtime-close")
					return runtimeCloseErr
				}),
			}, nil
		}),
		Integrations: integrationFactoryFunc(func(_ context.Context, req IntegrationRequest) (PlatformComponents, error) {
			*events = append(*events, "integrations")
			return req.Platforms, nil
		}),
		Executor: executorFunc(func(context.Context, PlatformRunRequest) error {
			*events = append(*events, "executor")
			return runErr
		}),
	}
	runner, err := NewRunner(deps)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	return runner
}
