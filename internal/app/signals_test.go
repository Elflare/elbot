package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/fileops"
	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage/sqlite"
)

type signalPlatform struct {
	name      string
	connected *signal.Signal[platform.ConnectedEvent]
}

func (p *signalPlatform) Name() string                                      { return p.name }
func (*signalPlatform) Run(context.Context, platform.PlatformHandler) error { return nil }
func (*signalPlatform) SendChat(context.Context, []delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}
func (*signalPlatform) SendNotice(context.Context, delivery.Notice) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}
func (p *signalPlatform) ConnectedSignal() *signal.Signal[platform.ConnectedEvent] {
	return p.connected
}

type signalAgent struct {
	notify func(context.Context, string)
}

func (a *signalAgent) NotifyPlatformConnected(ctx context.Context, name string) { a.notify(ctx, name) }

func TestPlatformSignalsAreIsolatedAndCancelledOnClose(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	one := &signalPlatform{name: "one", connected: signal.New[platform.ConnectedEvent]("one", logger)}
	two := &signalPlatform{name: "two", connected: signal.New[platform.ConnectedEvent]("two", logger)}
	started, second, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	agt := &signalAgent{notify: func(ctx context.Context, name string) {
		if name == "one" {
			close(started)
			<-ctx.Done()
			close(cancelled)
		} else if name == "two" {
			close(second)
		} else {
			t.Errorf("name=%q", name)
		}
	}}
	b := &signalBindings{}
	if err := b.connectPlatforms(agt, []platformRuntime{one, two}, logger); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := one.connected.Emit(ctx, platform.ConnectedEvent{Platform: "one"}); err != nil {
		t.Fatal(err)
	}
	<-started // FollowExecutor ignores the original emission's cancellation.
	if err := two.connected.Emit(context.Background(), platform.ConnectedEvent{}); err != nil {
		t.Fatal(err)
	}
	<-second
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-cancelled
	if !b.stopped() {
		t.Fatal("incomplete ownership/close")
	}
	if err := one.connected.Emit(context.Background(), platform.ConnectedEvent{Platform: "one"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerCleansSignalBindingsOnAttachFailure(t *testing.T) {
	var events []string
	runner := newTestRunner(t, &events, RunModeFull, "")
	b := &signalBindings{}
	started := make(chan struct{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := &signalPlatform{name: "one", connected: signal.New[platform.ConnectedEvent]("one", logger)}
	runner.deps.Runtime = runtimeFactoryFunc(func(context.Context, RuntimeRequest) (*RuntimeComponents, error) {
		return &RuntimeComponents{Handler: handlerStub{}, Signals: b, Lifecycle: lifecycleFunc(func(context.Context) error { return nil })}, nil
	})
	want := errors.New("attach failed")
	runner.deps.Integrations = integrationFactoryFunc(func(_ context.Context, req IntegrationRequest) (PlatformComponents, error) {
		agt := &signalAgent{notify: func(ctx context.Context, _ string) { close(started); <-ctx.Done() }}
		if err := b.connectPlatforms(agt, []platformRuntime{p}, logger); err != nil {
			return PlatformComponents{}, err
		}
		if err := p.connected.Emit(context.Background(), platform.ConnectedEvent{}); err != nil {
			return PlatformComponents{}, err
		}
		<-started
		return req.Platforms, want
	})
	if err := runner.Run(context.Background(), Options{}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if !b.stopped() {
		t.Fatal("signal callback leaked after failed startup")
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
	if err := bindings.connectSession(sessions, rollback, nil); err != nil {
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
