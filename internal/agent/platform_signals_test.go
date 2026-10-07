package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func newPlatformEventAgent(t *testing.T, handler hook.HandlerFunc) *Agent {
	t.Helper()
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformConnected, Name: "connection", Match: hook.Always(), Handler: handler}); err != nil {
		t.Fatal(err)
	}
	return newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) { opts.HookManager = manager })
}

func emitPlatformConnection(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	if err := globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: name}); err != nil {
		t.Fatal(err)
	}
}

func awaitPlatformEvent(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("platform event timed out")
	}
}

func TestGlobalPlatformHooksIsolatePlatformsAndCancelPending(t *testing.T) {
	type valueKey struct{}
	started, second := make(chan struct{}, 1), make(chan struct{}, 2)
	var firstCalls atomic.Int32
	a := newPlatformEventAgent(t, func(ctx context.Context, event hook.Event) (hook.Event, error) {
		if ctx.Value(valueKey{}) != "retained" || ctx.Err() != nil {
			t.Error("emission values or cancellation policy changed")
		}
		if event.Platform.ScopeID != "" || event.Actor.ID != "" {
			t.Error("invented chat identity")
		}
		if event.Platform.Name == "one" {
			firstCalls.Add(1)
			started <- struct{}{}
			<-ctx.Done()
		} else {
			second <- struct{}{}
		}
		return event, nil
	})
	for range 2 {
		if err := a.StartPlatformEvents(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), valueKey{}, "retained"))
	cancel()
	emitPlatformConnection(t, ctx, "one")
	awaitPlatformEvent(t, started)
	emitPlatformConnection(t, ctx, "one") // Pending work must be discarded on close.
	for range 2 {
		emitPlatformConnection(t, ctx, "two")
		awaitPlatformEvent(t, second)
	}
	closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := a.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if firstCalls.Load() != 1 {
		t.Fatalf("one calls=%d", firstCalls.Load())
	}
	emitPlatformConnection(t, ctx, "two")
	select {
	case <-second:
		t.Fatal("closed hook ran")
	default:
	}
	if err := a.StartPlatformEvents(t.Context()); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("restart=%v", err)
	}
}

type platformRecoveryRepo struct {
	storage.CronJobRepository
	seen chan struct{}
}

func (r *platformRecoveryRepo) ListEnabled(ctx context.Context) ([]storage.CronJob, error) {
	select {
	case r.seen <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
}

type platformRecoveryStore struct {
	storage.Store
	repo *platformRecoveryRepo
}

func (s platformRecoveryStore) CronJobs() storage.CronJobRepository { return s.repo }

func TestGlobalPlatformCronIndependentOfUserHooks(t *testing.T) {
	for _, mode := range []string{"stop", "error", "block"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			var once sync.Once
			a := newPlatformEventAgent(t, func(ctx context.Context, event hook.Event) (hook.Event, error) {
				once.Do(func() { close(started) })
				switch mode {
				case "stop":
					event.Control.StopPropagation = true
				case "error":
					return event, errors.New("user failure")
				case "block":
					<-ctx.Done()
				}
				return event, nil
			})
			repo := &platformRecoveryRepo{seen: make(chan struct{}, 2)}
			cron := elcron.NewService(elcron.Options{Store: platformRecoveryStore{repo: repo}})
			t.Cleanup(func() {
				if err := cron.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			if err := a.StartPlatformEvents(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := cron.StartPlatformEvents(t.Context()); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				emitPlatformConnection(t, context.Background(), "qqonebot")
				awaitPlatformEvent(t, repo.seen)
			}
			awaitPlatformEvent(t, started)
		})
	}
}

func TestPlatformHookCloseWaitsForActualExit(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	a := newPlatformEventAgent(t, func(ctx context.Context, event hook.Event) (hook.Event, error) {
		close(started)
		<-release
		return event, nil
	})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	if err := a.StartPlatformEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	emitPlatformConnection(t, context.Background(), "one")
	awaitPlatformEvent(t, started)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close=%v", err)
	}
	select {
	case <-a.Done():
		t.Fatal("Done closed with live hook")
	default:
	}
	// The disconnected subscription must not enqueue into a new queue.
	emitPlatformConnection(t, context.Background(), "two")
	once.Do(func() { close(release) })
	awaitPlatformEvent(t, a.Done())
}

func TestPlatformHookParentCancellationAndConcurrentClose(t *testing.T) {
	for _, mode := range []string{"parent", "close"} {
		t.Run(mode, func(t *testing.T) {
			a := newPlatformEventAgent(t, func(_ context.Context, event hook.Event) (hook.Event, error) { return event, nil })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := a.StartPlatformEvents(ctx); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range 50 {
						if err := globalevents.PlatformConnected.Emit(context.Background(), globalevents.PlatformConnectedEvent{Platform: "one"}); err != nil {
							t.Error(err)
						}
					}
				}()
			}
			if mode == "parent" {
				cancel()
			} else {
				a.BeginClose()
			}
			wg.Wait()
			awaitPlatformEvent(t, a.hooks.platformEventsDone())
			emitPlatformConnection(t, context.Background(), "two")
			if err := a.StartPlatformEvents(context.Background()); !errors.Is(err, signal.ErrClosed) {
				t.Fatalf("restart=%v", err)
			}
		})
	}
}

type platformOutputSender struct {
	fakePlatform
	sent chan delivery.Notice
}

func (p *platformOutputSender) SendNotice(_ context.Context, notice delivery.Notice) (delivery.Receipt, error) {
	p.sent <- notice
	return delivery.Receipt{}, nil
}
func TestGlobalPlatformHookSendsOutput(t *testing.T) {
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformConnected, Name: "output", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) {
		e.Outputs = []delivery.Output{delivery.Text("connected")}
		return e, nil
	})}); err != nil {
		t.Fatal(err)
	}
	p := &platformOutputSender{sent: make(chan delivery.Notice, 2)}
	a := newTestAgent(t, p, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) { opts.HookManager = manager })
	if err := a.StartPlatformEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		emitPlatformConnection(t, context.Background(), "cli")
		select {
		case notice := <-p.sent:
			if delivery.FallbackOutput(notice.Outputs).Text != "connected" {
				t.Fatalf("notice=%#v", notice)
			}
		case <-time.After(time.Second):
			t.Fatal("Hook output was not sent")
		}
	}
}

func TestPlatformConsumersRejectCapturedSnapshotAfterClose(t *testing.T) {
	captured, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	// Registration order pauses Emit after its snapshot includes both consumers.
	barrier, err := globalevents.PlatformConnected.Connect(func(context.Context, globalevents.PlatformConnectedEvent) error {
		close(captured)
		<-release
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Disconnect()
	var hookCalls atomic.Int32
	a := newPlatformEventAgent(t, func(_ context.Context, event hook.Event) (hook.Event, error) {
		hookCalls.Add(1)
		return event, nil
	})
	repo := &platformRecoveryRepo{seen: make(chan struct{}, 1)}
	cron := elcron.NewService(elcron.Options{Store: platformRecoveryStore{repo: repo}})
	t.Cleanup(func() { _ = cron.Close(context.Background()) })
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	if err := a.StartPlatformEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := cron.StartPlatformEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	emitted := make(chan error, 1)
	go func() {
		emitted <- globalevents.PlatformConnected.Emit(context.Background(), globalevents.PlatformConnectedEvent{Platform: "late"})
	}()
	awaitPlatformEvent(t, captured)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cron.Close(ctx); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-emitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("late snapshot did not return")
	}
	if hookCalls.Load() != 0 {
		t.Fatal("late snapshot started Hook")
	}
	select {
	case <-repo.seen:
		t.Fatal("late snapshot started Cron")
	default:
	}
}
