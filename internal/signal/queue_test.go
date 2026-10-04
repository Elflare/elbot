package signal

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func testQueue(t *testing.T, capacity int) *Queue {
	t.Helper()
	q, err := NewQueue(QueueOptions{Name: t.Name(), Capacity: capacity, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := q.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return q
}

func submit(t *testing.T, q *Queue, ctx context.Context, fn func(context.Context) error) {
	t.Helper()
	if err := q.Submit(ctx, Task{Run: fn, Shutdown: Drain}); err != nil {
		t.Fatal(err)
	}
}

func TestQueueCapacityFIFOAndErrorContinuation(t *testing.T) {
	q := testQueue(t, 2)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls []int
	submit(t, q, context.Background(), func(context.Context) error {
		close(started)
		<-release
		calls = append(calls, 0)
		return errors.New("expected failure")
	})
	<-started
	for i := 1; i <= 2; i++ {
		submit(t, q, context.Background(), func(context.Context) error { calls = append(calls, i); return nil })
	}
	if err := q.Submit(context.Background(), Task{Run: func(context.Context) error { return nil }}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full = %v", err)
	}
	close(release)
	if err := q.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []int{0, 1, 2}) {
		t.Fatal(calls)
	}
	if err := q.Submit(context.Background(), Task{Run: func(context.Context) error { return nil }}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestQueueLifetimeAndDisconnect(t *testing.T) {
	type key struct{}
	q := testQueue(t, 4)
	started := make(chan struct{})
	release := make(chan struct{})
	submit(t, q, context.Background(), func(context.Context) error { close(started); <-release; return nil })
	<-started
	s := New[int]("lifetime", testLogger())
	var requestCalls atomic.Int32
	got := make(chan int, 1)
	request := connect(t, s, func(context.Context, int) error { requestCalls.Add(1); return nil }, ConnectOptions{Executor: q, Lifetime: FollowEmit, Shutdown: Drain})
	service := connect(t, s, func(ctx context.Context, n int) error {
		if ctx.Err() != nil {
			t.Error(ctx.Err())
		}
		got <- ctx.Value(key{}).(int) + n
		return nil
	}, ConnectOptions{Executor: q, Lifetime: FollowExecutor, Shutdown: Drain})
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, 40))
	if err := s.Emit(ctx, 2); err != nil {
		t.Fatal(err)
	}
	cancel()
	request.Disconnect()
	service.Disconnect()
	close(release)
	if err := q.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requestCalls.Load() != 0 {
		t.Fatal("cancelled request ran")
	}
	if value := <-got; value != 42 {
		t.Fatal(value)
	}
}

func TestQueueTimeoutCancelsButDoesNotClaimStopped(t *testing.T) {
	q := testQueue(t, 2)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var pending atomic.Int32
	submit(t, q, context.Background(), func(ctx context.Context) error { close(started); <-ctx.Done(); close(cancelled); <-release; return nil })
	<-started
	submit(t, q, context.Background(), func(context.Context) error { pending.Add(1); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-cancelled
	select {
	case <-q.Done():
		t.Fatal("reported stopped while callback running")
	default:
	}
	if len(q.jobs) != 0 {
		t.Fatal("retained pending jobs")
	}
	close(release)
	<-q.Done()
	if pending.Load() != 0 {
		t.Fatal("pending job ran after timeout")
	}
	if err := q.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestQueueIsolationAndDefaults(t *testing.T) {
	if _, err := NewQueue(QueueOptions{Capacity: -1}); err == nil {
		t.Fatal("negative capacity accepted")
	}
	a := testQueue(t, 0)
	b := testQueue(t, 1)
	if cap(a.jobs) != 256 {
		t.Fatal(cap(a.jobs))
	}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	submit(t, a, context.Background(), func(context.Context) error { close(started); <-release; return nil })
	<-started
	submit(t, b, context.Background(), func(context.Context) error { close(done); return nil })
	<-done
	close(release)
}
