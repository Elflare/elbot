package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	elcron "elbot/internal/cron"
	globalevents "elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

type connectionCronRepo struct {
	storage.CronJobRepository
	seen chan struct{}
}

func (r *connectionCronRepo) ListEnabled(ctx context.Context) ([]storage.CronJob, error) {
	select {
	case r.seen <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
}

type connectionCronStore struct {
	storage.Store
	repo *connectionCronRepo
}

func (s connectionCronStore) CronJobs() storage.CronJobRepository { return s.repo }

type connectedAssemblyPlatform struct{ *assemblyPlatform }

func (p connectedAssemblyPlatform) Run(ctx context.Context, _ platform.PlatformHandler) error {
	return globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: p.Name()})
}

func TestProductionSubscribesBeforePlatformStarts(t *testing.T) {
	req, p, _ := runtimeAssemblyFixture(t)
	req.Platforms.Runtimes = []platform.Runtime{connectedAssemblyPlatform{p}}
	runtime, err := (defaultRuntimeFactory{}).Build(t.Context(), req)
	t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
	if err != nil {
		t.Fatal(err)
	}
	repo := &connectionCronRepo{seen: make(chan struct{}, 2)}
	runtime.CronService = elcron.NewService(elcron.Options{Store: connectionCronStore{repo: repo}})
	runtime.Lifecycle.(*runtimeLifecycle).cron = runtime.CronService
	platforms, err := (defaultIntegrationFactory{}).Attach(t.Context(), IntegrationRequest{
		Foundation: req.Foundation, Runtime: runtime, Platforms: req.Platforms, Mode: RunModeFull, Profiler: req.Profiler,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runPlatforms(t.Context(), runtime.Handler, platforms.Runtimes, nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repo.seen:
	case <-time.After(time.Second):
		t.Fatal("first connection was missed")
	}
	closeAssembledRuntime(t, runtime)
	if err := globalevents.PlatformConnected.Emit(context.Background(), globalevents.PlatformConnectedEvent{Platform: "cli"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repo.seen:
		t.Fatal("closed runtime consumed a connection")
	default:
	}
}

type blockedConnectionRepo struct {
	storage.CronJobRepository
	started, release chan struct{}
}

func (r *blockedConnectionRepo) ListEnabled(context.Context) ([]storage.CronJob, error) {
	close(r.started)
	<-r.release
	return nil, nil
}

type blockedConnectionStore struct {
	storage.Store
	repo *blockedConnectionRepo
}

func (s blockedConnectionStore) CronJobs() storage.CronJobRepository { return s.repo }

func TestRunnerDeadlineRetainsLivePlatformConsumerDependencies(t *testing.T) {
	var events []string
	runner := newTestRunner(t, &events, RunModeFull, "")
	runner.shutdownTimeout = 20 * time.Millisecond
	repo := &blockedConnectionRepo{started: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(repo.release) }) })
	cron := elcron.NewService(elcron.Options{Store: blockedConnectionStore{repo: repo}})
	lifecycle := &runtimeLifecycle{cancel: func() {}, cron: cron}
	runner.deps.Runtime = runtimeFactoryFunc(func(context.Context, RuntimeRequest) (*RuntimeComponents, error) {
		return &RuntimeComponents{Handler: handlerStub{}, CronService: cron, Lifecycle: lifecycle}, nil
	})
	runner.deps.Integrations = integrationFactoryFunc(func(ctx context.Context, req IntegrationRequest) (PlatformComponents, error) {
		return req.Platforms, cron.StartPlatformEvents(ctx)
	})
	runner.deps.Executor = executorFunc(func(ctx context.Context, _ PlatformRunRequest) error {
		if err := globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: "one"}); err != nil {
			return err
		}
		select {
		case <-repo.started:
		case <-time.After(time.Second):
			return errors.New("consumer did not start")
		}
		return nil
	})
	start := time.Now()
	if err := runner.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("shutdown exceeded shared budget")
	}
	if lifecycle.stopped() {
		t.Fatal("live consumer reported stopped")
	}
	for _, event := range events {
		if event == "foundation-close" || event == "marker-close" {
			t.Fatalf("released live dependency: %s", event)
		}
	}
	if err := globalevents.PlatformConnected.Emit(context.Background(), globalevents.PlatformConnectedEvent{Platform: "two"}); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(repo.release) })
	select {
	case <-cron.Done():
	case <-time.After(time.Second):
		t.Fatal("worker did not exit")
	}
}
