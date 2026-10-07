package cron

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	globalevents "elbot/internal/events"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func startCronPlatformEvents(t *testing.T, s *Service) {
	t.Helper()
	if err := s.StartPlatformEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
}

// A marker in the same worker lets the business regressions inspect their fake
// repository only after recovery has finished, without sleeps or polling.
func emitCronPlatformAndWait(t *testing.T, s *Service, name string) {
	t.Helper()
	if err := globalevents.PlatformConnected.Emit(context.Background(), globalevents.PlatformConnectedEvent{Platform: name}); err != nil {
		t.Fatal(err)
	}
	s.platformEvents.mu.Lock()
	queue := s.platformEvents.platformQueues[name]
	s.platformEvents.mu.Unlock()
	if queue == nil {
		t.Fatal("missing platform worker")
	}
	done := make(chan struct{})
	if err := queue.Submit(context.Background(), signal.Task{Shutdown: signal.CancelPending, Run: func(context.Context) error { close(done); return nil }}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery did not complete")
	}
}

type connectionRepo struct {
	storage.CronJobRepository
	list func(context.Context) ([]storage.CronJob, error)
}

func (r connectionRepo) ListEnabled(ctx context.Context) ([]storage.CronJob, error) {
	return r.list(ctx)
}

type connectionStore struct {
	storage.Store
	repo storage.CronJobRepository
}

func (s connectionStore) CronJobs() storage.CronJobRepository { return s.repo }

func TestCronPlatformSubscriptionsIsolateAndCancel(t *testing.T) {
	type key struct{}
	first, second := make(chan struct{}, 1), make(chan struct{}, 1)
	var calls atomic.Int32
	s := NewService(Options{Store: connectionStore{repo: connectionRepo{list: func(ctx context.Context) ([]storage.CronJob, error) {
		if ctx.Value(key{}) == "one" {
			calls.Add(1)
			first <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		second <- struct{}{}
		return nil, nil
	}}}})
	startCronPlatformEvents(t, s)
	if err := s.StartPlatformEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	emit := func(name string) {
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, name))
		cancel()
		if err := globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: name}); err != nil {
			t.Fatal(err)
		}
	}
	emit("one")
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first platform did not start")
	}
	emit("one")
	emit("two")
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("one platform blocked another")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	emit("three")
	select {
	case <-second:
		t.Fatal("consumed after close")
	default:
	}
	if err := s.StartPlatformEvents(context.Background()); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("restart=%v", err)
	}
}

func TestCronPlatformSubscriptionRejectsBlankAndDoesNotReplay(t *testing.T) {
	var calls atomic.Int32
	s := NewService(Options{Store: connectionStore{repo: connectionRepo{list: func(context.Context) ([]storage.CronJob, error) { calls.Add(1); return nil, nil }}}})
	if err := globalevents.PlatformConnected.Emit(context.Background(), globalevents.PlatformConnectedEvent{Platform: "before"}); err != nil {
		t.Fatal(err)
	}
	startCronPlatformEvents(t, s)
	if err := globalevents.PlatformConnected.Emit(context.Background(), globalevents.PlatformConnectedEvent{Platform: " "}); err == nil {
		t.Fatal("blank platform accepted")
	}
	emitCronPlatformAndWait(t, s, "after")
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestCronPlatformSubscriptionParentCancellationRacesEmission(t *testing.T) {
	s := NewService(Options{Store: connectionStore{repo: connectionRepo{list: func(context.Context) ([]storage.CronJob, error) { return nil, nil }}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.StartPlatformEvents(ctx); err != nil {
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
	cancel()
	wg.Wait()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("parent cancellation leaked subscription")
	}
	if err := s.StartPlatformEvents(context.Background()); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("restart=%v", err)
	}
}
