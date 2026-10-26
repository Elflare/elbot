package cron

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/signal"
)

func TestPlatformStartCoalescesLatestContextAndReturnsBeforeRecovery(t *testing.T) {
	type key struct{}
	values := make(chan string, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	s := newTestCronService(t, Options{Store: connectionRepo{list: func(ctx context.Context) ([]Job, error) {
		calls.Add(1)
		values <- ctx.Value(key{}).(string)
		<-release
		return nil, nil
	}}})
	for _, value := range []string{"old", "latest"} {
		ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, value))
		cancel()
		if err := s.EnqueuePlatformConnected(ctx, "one"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnqueuePlatformConnected(context.WithValue(t.Context(), key{}, "two"), "two"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || len(s.platformEvents.platformQueues) != 0 {
		t.Fatal("work started before Start")
	}
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case value := <-values:
			seen[value] = true
		case <-time.After(time.Second):
			t.Fatal("cached recovery not started")
		}
	}
	if !seen["latest"] || !seen["two"] || calls.Load() != 2 {
		t.Fatalf("contexts=%v calls=%d", seen, calls.Load())
	}
	close(release)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformStartupFailureOrCloseDropsCachedWork(t *testing.T) {
	for _, cancelStart := range []bool{false, true} {
		t.Run(map[bool]string{false: "close before start", true: "canceled start"}[cancelStart], func(t *testing.T) {
			var calls atomic.Int32
			s := newTestCronService(t, Options{Store: connectionRepo{list: func(context.Context) ([]Job, error) { calls.Add(1); return nil, nil }}})
			if err := s.EnqueuePlatformConnected(t.Context(), "one"); err != nil {
				t.Fatal(err)
			}
			if cancelStart {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if err := s.Start(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("start=%v", err)
				}
			} else {
				s.BeginClose()
			}
			awaitCronDone(t, s.Done())
			if err := s.EnqueuePlatformConnected(t.Context(), "late"); err != nil {
				t.Fatal(err)
			}
			if err := s.Start(t.Context()); !errors.Is(err, signal.ErrClosed) {
				t.Fatalf("restart=%v", err)
			}
			if calls.Load() != 0 || len(s.platformEvents.pendingPlatforms) != 0 || len(s.platformEvents.platformQueues) != 0 {
				t.Fatal("closed consumer retained work")
			}
		})
	}
}

func TestPlatformCloseDeadlineDoesNotFinishRunningCallback(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	s := newTestCronService(t, Options{Store: connectionRepo{list: func(context.Context) ([]Job, error) { close(started); <-release; return nil, nil }}})
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueuePlatformConnected(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	awaitCronDone(t, started)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close=%v", err)
	}
	select {
	case <-s.Done():
		t.Fatal("Done closed with callback running")
	default:
	}
	close(release)
	awaitCronDone(t, s.Done())
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
