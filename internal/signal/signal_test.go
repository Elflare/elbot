package signal

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
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
	e, err := s.emit(context.Background(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	results, err := e.Wait(context.Background())
	if err != nil || !errors.Is(results[0].Err, context.DeadlineExceeded) || !results[0].Started {
		t.Fatal(results, err)
	}
	queued, err := s.emit(context.Background(), 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.emit(context.Background(), 2, true); !errors.Is(err, ErrQueueFull) {
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
	a, _ := s.emit(context.Background(), 0, true)
	<-started
	b, _ := s.emit(context.Background(), 1, true)
	c, _ := s.emit(context.Background(), 2, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if r, err := s.Emit(ctx, 3); err != nil || r[0].Value != 3 {
		t.Fatal(r, err)
	}
	close(release)
	for _, e := range []*emission[int]{a, b, c} {
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
	_, err := up.ConnectForward(func(ctx context.Context, n int) (context.Context, *Signal[int, int], int) {
		down := fast
		if n == 0 {
			down = slow
		}
		return ctx, down, n
	}, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := up.emit(context.Background(), 0, true)
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
	a, _ := s.emit(context.Background(), 0, true)
	<-started
	b, _ := s.emit(context.Background(), 1, true)
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
	s := New[int, Empty]("async", Options[int]{Capacity: 1, Concurrency: 1})
	entered, release := make(chan struct{}), make(chan struct{})
	_, err := s.Observe(func(context.Context, int) { close(entered); <-release }, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	defer close(release)
	s.EmitAsync(t.Context(), 1)
	<-entered
	s.EmitAsync(t.Context(), 2)
	s.EmitAsync(t.Context(), 3)
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
	s.EmitAsync(t.Context(), 4)
}

func TestBusinessFailureIsReportedOnlyByObserver(t *testing.T) {
	if err := FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "reports")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = file
	defer func() { _ = FlushDiagnostics(context.Background()); os.Stderr = previous; _ = file.Close() }()
	logger := slog.New(slog.NewTextHandler(file, nil))
	failure := errors.New("business failure owned by receiver")
	s := testSignal[int, Empty](t, Options[int]{})
	connection, err := s.Observe(func(ctx context.Context, _ int) {
		call := func() error { return failure }
		if err := call(); err != nil {
			logger.ErrorContext(ctx, err.Error())
		}
	}, ConnectOptions{Shutdown: Drain})
	if err != nil {
		t.Fatal(err)
	}
	s.EmitAsync(t.Context(), 0)
	if err := connection.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil || strings.Count(string(data), failure.Error()) != 1 || strings.Contains(string(data), "signal delivery failed") {
		t.Fatalf("reports=%s error=%v", data, err)
	}
}

func TestCallbackPanicIsNotConvertedToSignalError(t *testing.T) {
	if os.Getenv("ELBOT_SIGNAL_PANIC_HELPER") == "1" {
		s := New[int, Empty]("panic")
		_, _ = s.Observe(func(context.Context, int) { panic("business-panic-sentinel") }, ConnectOptions{})
		s.EmitAsync(context.Background(), 0)
		select {}
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestCallbackPanicIsNotConvertedToSignalError$")
	cmd.Env = append(os.Environ(), "ELBOT_SIGNAL_PANIC_HELPER=1")
	data, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(data), "panic: business-panic-sentinel") {
		t.Fatalf("panic not exposed: %s error=%v", data, err)
	}
}

func TestAsyncRejectsSynchronousHandlersBeforeDispatch(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{})
	var calls atomic.Int32
	_, err := s.Observe(func(context.Context, int) { calls.Add(1) }, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("business failure")
	connect(t, s, func(context.Context, int) (int, error) { calls.Add(1); return 3, failure }, ConnectOptions{})
	if _, err := s.emit(t.Context(), 0, false); err == nil || errors.Is(err, failure) {
		t.Fatalf("expected framework contract error, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("mixed async emission dispatched callbacks")
	}
	results, err := s.Emit(t.Context(), 0)
	if err != nil || len(results) != 2 || results[1].Value != 3 || results[1].Err != failure || calls.Load() != 2 {
		t.Fatal(results, err, calls.Load())
	}
}

func TestAsyncLifetimeDoesNotCreateResultTracking(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e := newEmission[int](cancel, 2, false)
	if e.results != nil || e.finished != nil || e.started != nil || e.done != nil || e.callbacks != nil || e.remaining != 0 {
		t.Fatal("async emission created synchronous result state")
	}
	exited := false
	e.onExit(func() { exited = true })
	e.exit()
	if ctx.Err() != nil || exited {
		t.Fatal("lifetime ended before actual callbacks exited")
	}
	e.exit()
	if ctx.Err() != context.Canceled || !exited {
		t.Fatal("actual exit did not release lifetime")
	}
}

func TestAsyncQueuedCancellationDoesNotExecuteObserver(t *testing.T) {
	s := testSignal[int, Empty](t, Options[int]{Concurrency: 1})
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	_, err := s.Observe(func(_ context.Context, n int) {
		calls.Add(1)
		if n == 0 {
			close(started)
			<-release
		}
	}, ConnectOptions{Shutdown: Drain})
	if err != nil {
		t.Fatal(err)
	}
	s.EmitAsync(t.Context(), 0)
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	s.EmitAsync(ctx, 1)
	cancel()
	close(release)
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("canceled queued observer executed")
	}
}
