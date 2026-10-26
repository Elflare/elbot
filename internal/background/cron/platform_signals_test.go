package cron

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/signal"
)

func startCronPlatformEvents(t *testing.T, s *Service) {
	t.Helper()
	if err := s.Start(t.Context()); err != nil {
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
	if err := s.EnqueuePlatformConnected(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	waitCronPlatform(t, s, name)
}

func waitCronPlatform(t *testing.T, s *Service, name string) {
	t.Helper()
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
	JobRepository
	list func(context.Context) ([]Job, error)
}

func (r connectionRepo) ListEnabled(ctx context.Context) ([]Job, error) {
	return r.list(ctx)
}

func TestCronPlatformSubscriptionsIsolateAndCancel(t *testing.T) {
	type key struct{}
	first, second := make(chan struct{}, 1), make(chan struct{}, 1)
	var calls atomic.Int32
	s := newTestCronService(t, Options{Store: connectionRepo{list: func(ctx context.Context) ([]Job, error) {
		if ctx.Value(key{}) == "one" {
			calls.Add(1)
			first <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		second <- struct{}{}
		return nil, nil
	}}})
	startCronPlatformEvents(t, s)
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	emit := func(name string) {
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, name))
		if err := s.EnqueuePlatformConnected(ctx, name); err != nil {
			t.Fatal(err)
		}
		cancel()
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
	if err := s.Start(context.Background()); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("restart=%v", err)
	}
}

func TestCronPlatformSubscriptionRejectsBlankAndCachesBeforeStart(t *testing.T) {
	var calls atomic.Int32
	s := newTestCronService(t, Options{Store: connectionRepo{list: func(context.Context) ([]Job, error) { calls.Add(1); return nil, nil }}})
	if err := s.EnqueuePlatformConnected(context.Background(), "before"); err != nil {
		t.Fatal(err)
	}
	startCronPlatformEvents(t, s)
	waitCronPlatform(t, s, "before")
	if err := s.EnqueuePlatformConnected(context.Background(), " "); err == nil {
		t.Fatal("blank platform accepted")
	}
	emitCronPlatformAndWait(t, s, "after")
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestCronPlatformSubscriptionParentCancellationRacesEmission(t *testing.T) {
	s := newTestCronService(t, Options{Store: connectionRepo{list: func(context.Context) ([]Job, error) { return nil, nil }}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if err := s.EnqueuePlatformConnected(context.Background(), "one"); err != nil {
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
	if err := s.Start(context.Background()); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("restart=%v", err)
	}
}
