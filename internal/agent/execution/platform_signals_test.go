package execution

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	agentevents "elbot/internal/agent/events"
	agenthooks "elbot/internal/agent/hooks"
	agentshare "elbot/internal/agent/share"
	elcron "elbot/internal/cron"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/platform"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func newTestHookBridge(t *testing.T, p platform.PlatformAdapter, manager *hook.DefaultManager, failures ...*signal.Signal[agentevents.HookFailedEvent, signal.Empty]) *agenthooks.HookBridge {
	t.Helper()
	dispatcher := dispatch.New(dispatch.Options{Primary: p})
	connectFixtureNotifications(t, p.Name(), dispatcher)
	failed := agentevents.NewSignals().HookFailed
	if len(failures) > 0 {
		failed = failures[0]
	}
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{
		Manager: manager, Router: &fakeHookRouter{}, Requests: request.NewManager(0),
		Identity: agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: p.Name(), ActorID: "cli:local", ScopeID: "local", Permission: security.DefaultPermission()}),
		Failed:   failed, Dispatcher: dispatcher,
	})
	manager.SetObserver(bridge.ObserveRun)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := bridge.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return bridge
}

func newPlatformEventBridge(t *testing.T, handler hook.HandlerFunc) *agenthooks.HookBridge {
	t.Helper()
	manager := newTestHookManager()

	manager.Register(hook.Registration{Point: hook.PointPlatformConnected, Name: "connection", Match: hook.Always(), Handler: handler})
	return newTestHookBridge(t, &fakePlatform{}, manager)
}

func emitPlatformConnection(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	if _, err := globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: name}); err != nil {
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
	a := newPlatformEventBridge(t, func(ctx context.Context, event hook.Event) (hook.Event, globalevents.LogResult, error) {
		if ctx.Value(valueKey{}) != "retained" || ctx.Err() != nil {
			t.Error("emission values or cancellation permission changed")
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
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})
	for range 2 {
		if err := a.StartPlatformEvents(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), valueKey{}, "retained"))
	defer cancel()
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
			a := newPlatformEventBridge(t, func(ctx context.Context, event hook.Event) (hook.Event, globalevents.LogResult, error) {
				once.Do(func() { close(started) })
				switch mode {
				case "stop":
					event.Control.StopPropagation = true
				case "error":
					return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "test source failed"}, errors.New("user failure")
				case "block":
					<-ctx.Done()
				}
				return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
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
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		a := newPlatformEventBridge(t, func(ctx context.Context, event hook.Event) (hook.Event, globalevents.LogResult, error) {
			close(started)
			<-release
			return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
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
	})
}

func TestPlatformHookParentCancellationAndConcurrentClose(t *testing.T) {
	for _, mode := range []string{"parent", "close"} {
		t.Run(mode, func(t *testing.T) {
			a := newPlatformEventBridge(t, func(_ context.Context, event hook.Event) (hook.Event, globalevents.LogResult, error) {
				return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
			})
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
						if _, err := globalevents.PlatformConnected.Emit(context.Background(), globalevents.PlatformConnectedEvent{Platform: "one"}); err != nil {
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
			awaitPlatformEvent(t, a.Done())
			emitPlatformConnection(t, context.Background(), "two")
			if err := a.StartPlatformEvents(context.Background()); !errors.Is(err, signal.ErrClosed) {
				t.Fatalf("restart=%v", err)
			}
		})
	}
}

type platformOutputSender struct {
	fakePlatform
	sent chan []delivery.Output
}

func (p *platformOutputSender) SendTo(_ context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	p.sent <- noticeOutputs
	return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func TestGlobalPlatformHookSendsOutput(t *testing.T) {
	manager := newTestHookManager()

	manager.Register(hook.Registration{Point: hook.PointPlatformConnected, Name: "output", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, globalevents.LogResult, error) {
		e.Outputs = []delivery.Output{delivery.Text("connected")}
		return e, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})
	p := &platformOutputSender{sent: make(chan []delivery.Output, 2)}
	a := newTestHookBridge(t, p, manager)
	if err := a.StartPlatformEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		emitPlatformConnection(t, context.Background(), "cli")
		select {
		case notice := <-p.sent:
			if delivery.FallbackOutput(notice).Text != "connected" {
				t.Fatalf("notice=%#v", notice)
			}
		case <-time.After(time.Second):
			t.Fatal("Hook output was not sent")
		}
	}
}

func TestPlatformConsumersRejectCapturedSnapshotAfterClose(t *testing.T) {
	captured, release := make(chan struct{}, 8), make(chan struct{})
	var releaseOnce sync.Once
	// Occupy every execution slot; registration order does not serialize callbacks.
	barrier, err := globalevents.PlatformConnected.Observe(func(_ context.Context, e globalevents.PlatformConnectedEvent) error {
		if e.Platform == "barrier" {
			captured <- struct{}{}
			<-release
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Disconnect()
	defer releaseOnce.Do(func() { close(release) })
	for range 8 {
		if _, err := globalevents.PlatformConnected.EmitAsync(context.Background(), globalevents.PlatformConnectedEvent{Platform: "barrier"}); err != nil {
			t.Fatal(err)
		}
	}
	for range 8 {
		awaitPlatformEvent(t, captured)
	}
	var hookCalls atomic.Int32
	a := newPlatformEventBridge(t, func(_ context.Context, event hook.Event) (hook.Event, globalevents.LogResult, error) {
		hookCalls.Add(1)
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
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
	emitted, err := globalevents.PlatformConnected.EmitAsync(context.Background(), globalevents.PlatformConnectedEvent{Platform: "late"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cron.Close(ctx); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	if _, err := emitted.Wait(ctx); err != nil {
		t.Fatal(err)
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
