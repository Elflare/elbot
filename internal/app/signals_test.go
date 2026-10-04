package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/signal"
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
	senders []string
	notify  func(context.Context, string)
}

func (a *signalAgent) RegisterPlatformSender(name string, _ delivery.MessageSender) {
	a.senders = append(a.senders, name)
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
	if !b.stopped() || len(agt.senders) != 2 {
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
