package signal

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testSignal[T, R any](t *testing.T, o Options[T]) *Signal[T, R] {
	t.Helper()
	s := New[T, R](t.Name(), o)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}
func connect[T, R any](t *testing.T, s *Signal[T, R], h Handler[T, R], o ConnectOptions) *Connection {
	t.Helper()
	c, err := s.Connect(h, o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestResults(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{})
	connect(t, s, func(_ context.Context, n int) (int, error) { return n + 1, nil }, ConnectOptions{})
	want := errors.New("failed")
	connect(t, s, func(_ context.Context, n int) (int, error) { return n + 2, want }, ConnectOptions{})
	results, err := s.Emit(context.Background(), 4)
	if err != nil || len(results) != 2 || !errors.Is(results[1].Err, want) || results[0].Value != 5 || results[1].Value != 6 {
		t.Fatalf("results=%v err=%v", results, err)
	}
}
func TestTimeoutRetainsSlotAndOtherSignalsProgress(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{Concurrency: 1, Capacity: 1, Timeout: 30 * time.Millisecond})
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	connect(t, s, func(context.Context, int) (int, error) { calls.Add(1); close(started); <-release; return 9, nil }, ConnectOptions{})
	e, err := s.EmitAsync(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	results, err := e.Wait(context.Background())
	if err != nil || !errors.Is(results[0].Err, context.DeadlineExceeded) || !results[0].Started {
		t.Fatal(results, err)
	}
	queued, err := s.EmitAsync(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EmitAsync(context.Background(), 2); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	fast := testSignal[int, int](t, Options[int]{})
	connect(t, fast, func(_ context.Context, n int) (int, error) { return n, nil }, ConnectOptions{})
	if r, err := fast.Emit(context.Background(), 7); err != nil || r[0].Value != 7 {
		t.Fatal(r, err)
	}
	if r, err := queued.Wait(context.Background()); err != nil || !errors.Is(r[0].Err, context.DeadlineExceeded) || r[0].Started {
		t.Fatal(r, err)
	}
	if calls.Load() != 1 {
		t.Fatal("timed out callback lost its execution slot")
	}
	if results[0].Value != 0 {
		t.Fatal("late result mutated snapshot")
	}
}
func TestKeyOrderingDoesNotOccupyWorkers(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{Concurrency: 2, Key: func(n int) any {
		if n < 3 {
			return "same"
		}
		return "other"
	}})
	started, release := make(chan struct{}), make(chan struct{})
	var order []int
	var mu sync.Mutex
	connect(t, s, func(_ context.Context, n int) (int, error) {
		if n == 0 {
			close(started)
			<-release
		}
		mu.Lock()
		order = append(order, n)
		mu.Unlock()
		return n, nil
	}, ConnectOptions{})
	a, _ := s.EmitAsync(context.Background(), 0)
	<-started
	b, _ := s.EmitAsync(context.Background(), 1)
	c, _ := s.EmitAsync(context.Background(), 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if r, err := s.Emit(ctx, 3); err != nil || r[0].Value != 3 {
		t.Fatal(r, err)
	}
	close(release)
	for _, e := range []*Emission[int]{a, b, c} {
		if _, err := e.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 4 || order[0] != 3 || order[1] != 0 || order[2] != 1 || order[3] != 2 {
		t.Fatal(order)
	}
}
func TestForwardingReleasesUpstreamWorkerAndPreservesDeadline(t *testing.T) {
	up := testSignal[int, int](t, Options[int]{Concurrency: 1, Required: true, Single: true, Timeout: 200 * time.Millisecond})
	slow := testSignal[int, int](t, Options[int]{Single: true})
	fast := testSignal[int, int](t, Options[int]{Single: true})
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	connect(t, slow, func(ctx context.Context, _ int) (int, error) { close(started); <-release; return 1, ctx.Err() }, ConnectOptions{})
	connect(t, fast, func(_ context.Context, n int) (int, error) { return n, nil }, ConnectOptions{})
	_, err := up.ConnectForward(func(ctx context.Context, n int) *Emission[int] {
		down := fast
		if n == 0 {
			down = slow
		}
		e, err := down.EmitAsync(ctx, n)
		if e == nil {
			return Resolved(Result[int]{Err: err})
		}
		return e
	}, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := up.EmitAsync(context.Background(), 0)
	<-started
	if r, err := up.Emit(context.Background(), 5); err != nil || r[0].Value != 5 {
		t.Fatal(r, err)
	}
	if results, err := a.Wait(context.Background()); err != nil || !errors.Is(results[0].Err, context.DeadlineExceeded) || !results[0].Started {
		t.Fatal(results, err)
	}
}
func TestRecursiveWaitAndMissingReceiver(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{Required: true, Single: true})
	if _, err := s.Emit(context.Background(), 0); !errors.Is(err, ErrNoReceiver) {
		t.Fatal(err)
	}
	connect(t, s, func(ctx context.Context, n int) (int, error) { _, err := s.Emit(ctx, n); return 0, err }, ConnectOptions{})
	if results, err := s.Emit(context.Background(), 0); err != nil || !errors.Is(results[0].Err, ErrRecursive) {
		t.Fatal(err)
	}
	if _, err := s.Connect(func(context.Context, int) (int, error) { return 0, nil }, ConnectOptions{}); err == nil {
		t.Fatal("duplicate receiver accepted")
	}
}
func TestClosingCompletesPendingResults(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{Concurrency: 1})
	started, release := make(chan struct{}), make(chan struct{})
	connect(t, s, func(ctx context.Context, _ int) (int, error) {
		close(started)
		<-ctx.Done()
		<-release
		return 0, ctx.Err()
	}, ConnectOptions{})
	a, _ := s.EmitAsync(context.Background(), 0)
	<-started
	b, _ := s.EmitAsync(context.Background(), 1)
	s.BeginClose()
	if results, err := b.Wait(context.Background()); err != nil || results[0].Err == nil {
		t.Fatal("pending result not canceled")
	}
	if results, err := a.Wait(context.Background()); err != nil || !errors.Is(results[0].Err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
		t.Fatal("reported exit before callback returned")
	default:
	}
	close(release)
}

// Async publication must not borrow the producer's lifetime to wait for a callback.
func TestAsyncPublicationAndClose(t *testing.T) {
	s := NewAsync[int]("async", Options[int]{Capacity: 1, Concurrency: 1})
	entered, release := make(chan struct{}), make(chan struct{})
	_, err := s.Observe(func(context.Context, int) error { close(entered); <-release; return nil }, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	defer close(release)
	if err := s.Publish(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := s.Publish(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if err := s.Publish(t.Context(), 3); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full queue: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-s.Done():
		t.Fatal("closed before callback exit")
	default:
	}
	if err := s.Publish(t.Context(), 4); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed publication: %v", err)
	}
}
