package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	elcron "elbot/internal/cron"
	globalevents "elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage/sqlite"
)

func TestRunnerCleansPlatformSubscriptionsOnAttachFailure(t *testing.T) {
	var events []string
	runner := newTestRunner(t, &events, RunModeFull, "")
	repo := &connectionCronRepo{seen: make(chan struct{}, 1)}
	cron := elcron.NewService(elcron.Options{Store: connectionCronStore{repo: repo}})
	lifecycle := &runtimeLifecycle{cancel: func() {}, cron: cron}
	runner.deps.Runtime = runtimeFactoryFunc(func(context.Context, RuntimeRequest) (*RuntimeComponents, error) {
		return &RuntimeComponents{Handler: handlerStub{}, CronService: cron, Lifecycle: lifecycle}, nil
	})
	want := errors.New("attach failed")
	runner.deps.Integrations = integrationFactoryFunc(func(ctx context.Context, req IntegrationRequest) (PlatformComponents, error) {
		if err := cron.StartPlatformEvents(ctx); err != nil {
			return PlatformComponents{}, err
		}
		if err := globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: "one"}); err != nil {
			return PlatformComponents{}, err
		}
		select {
		case <-repo.seen:
		case <-time.After(time.Second):
			t.Fatal("connection not consumed")
		}
		return req.Platforms, want
	})
	if err := runner.Run(context.Background(), Options{}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if !lifecycle.stopped() {
		t.Fatal("platform subscription leaked after failed startup")
	}
	if err := globalevents.PlatformConnected.Emit(context.Background(), globalevents.PlatformConnectedEvent{Platform: "one"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repo.seen:
		t.Fatal("closed consumer ran")
	default:
	}
	if err := cron.StartPlatformEvents(context.Background()); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("restart=%v", err)
	}
}

func TestRunnerDeadlineDoesNotCloseLiveCallbackDependencies(t *testing.T) {
	var events []string
	runner := newTestRunner(t, &events, RunModeFull, "")
	runner.shutdownTimeout = 20 * time.Millisecond
	q, err := signal.NewQueue(signal.QueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b := &signalBindings{queues: []*signal.Queue{q}}
	started, release := make(chan struct{}), make(chan struct{})
	runtimeClosed := false
	runner.deps.Runtime = runtimeFactoryFunc(func(context.Context, RuntimeRequest) (*RuntimeComponents, error) {
		return &RuntimeComponents{Handler: handlerStub{}, Signals: b, Lifecycle: lifecycleFunc(func(context.Context) error { runtimeClosed = true; return nil })}, nil
	})
	runner.deps.Executor = executorFunc(func(context.Context, PlatformRunRequest) error {
		if err := q.Submit(context.Background(), signal.Task{Run: func(context.Context) error { close(started); <-release; return nil }}); err != nil {
			return err
		}
		<-started
		return nil
	})
	before := time.Now()
	if err := runner.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if time.Since(before) > time.Second {
		t.Fatal("shutdown exceeded budget")
	}
	if runtimeClosed || b.stopped() {
		t.Fatal("live callback reported closed or dependencies released")
	}
	for _, event := range events {
		if event == "foundation-close" || event == "marker-close" {
			t.Fatalf("released live dependency: %s", event)
		}
	}
	close(release)
	<-q.Done()
	if runtimeClosed {
		t.Fatal("unexpected background cleanup")
	}
}

func TestRunnerRetainsRealErrorAlongsideCancellation(t *testing.T) {
	var events []string
	runner := newTestRunner(t, &events, RunModeFull, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := errors.New("real failure")
	runner.deps.Executor = executorFunc(func(context.Context, PlatformRunRequest) error { cancel(); return errors.Join(context.Canceled, want) })
	err := runner.Run(ctx, Options{})
	if !errors.Is(err, want) || errors.Is(err, context.Canceled) {
		t.Fatalf("Run=%v", err)
	}
}

func TestDelayedSessionCleanupCannotInvalidateNewRollbackBinding(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.NewService(store)
	scope := session.Scope{ActorID: "u", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	row, err := sessions.Create(ctx, scope, session.CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, old, err := sessions.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	rollback := fileops.NewRollbackManager()
	oldLease, _ := rollback.Session(old)
	bindings := &signalBindings{}
	if err := bindings.connectSession(sessions, rollback); err != nil {
		t.Fatal(err)
	}
	defer bindings.Close(ctx)
	started, release := make(chan struct{}), make(chan struct{})
	if err := bindings.queues[0].Submit(ctx, signal.Task{Run: func(ctx context.Context) error {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := sessions.ResetCurrent(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if old.Valid() {
		t.Fatal("binding remained valid while cleanup blocked")
	}
	if _, err := oldLease.List(); !errors.Is(err, fileops.ErrRollbackExpired) {
		t.Fatalf("old lease: %v", err)
	}
	if _, err := sessions.Resume(ctx, scope, row.ID); err != nil {
		t.Fatal(err)
	}
	_, current, err := sessions.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	lease, _ := rollback.Session(current)
	content := "new binding content"
	path := filepath.Join(t.TempDir(), "file")
	if _, err := lease.EditFile(ctx, path, "", "", true, 3, []fileops.Edit{{Operation: "overwrite", NewText: &content}}, fileops.EditFileOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	drained := make(chan struct{})
	if err := bindings.queues[0].Submit(ctx, signal.Task{Run: func(context.Context) error { close(drained); return nil }}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not drain")
	}
	items, err := lease.List()
	if err != nil || len(items) != 1 || !current.Valid() {
		t.Fatalf("new records: %#v %v", items, err)
	}
}
