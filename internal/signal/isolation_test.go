package signal

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBlockedSignalBoundedResourcesAndTailLatency(t *testing.T) {
	blocked := testSignal[int, Empty](t, Options[int]{Concurrency: 8, Capacity: 64})
	fast := testSignal[int, int](t, Options[int]{})
	started, release := make(chan struct{}, 8), make(chan struct{})
	defer close(release)
	connect(t, blocked, func(context.Context, int) (Empty, error) { started <- struct{}{}; <-release; return Empty{}, nil }, ConnectOptions{})
	connect(t, fast, func(_ context.Context, n int) (int, error) { return n, nil }, ConnectOptions{})
	for range 8 {
		if _, err := blocked.EmitAsync(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
	}
	for range 8 {
		<-started
	}
	queued, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 64 {
		if _, err := blocked.EmitAsync(queued, 0); err != nil {
			t.Fatal(err)
		}
	}
	base := runtime.NumGoroutine()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range 2000 {
		if _, err := blocked.EmitAsync(context.Background(), 0); !errors.Is(err, ErrQueueFull) {
			t.Fatal(err)
		}
	}
	latency := make([]time.Duration, 1000)
	for i := range latency {
		at := time.Now()
		if r, err := fast.Emit(context.Background(), i); err != nil || r[0].Value != i {
			t.Fatal(r, err)
		}
		latency[i] = time.Since(at)
	}
	sort.Slice(latency, func(i, j int) bool { return latency[i] < latency[j] })
	runtime.GC()
	runtime.ReadMemStats(&after)
	blocked.queue.mu.Lock()
	active, pending, workers := len(blocked.queue.active), blocked.queue.pending, blocked.queue.workers
	blocked.queue.mu.Unlock()
	if active != 8 || pending != 64 || workers != 8 {
		t.Fatalf("active=%d pending=%d workers=%d", active, pending, workers)
	}
	if growth := runtime.NumGoroutine() - base; growth > 24 {
		t.Fatalf("goroutine growth=%d", growth)
	}
	if growth := int64(after.HeapAlloc) - int64(before.HeapAlloc); growth > 8<<20 {
		t.Fatalf("retained heap growth=%d", growth)
	}
	t.Logf("blocked signal: active=%d pending=%d; independent signal P95=%s P99=%s; heap delta=%d bytes", active, pending, latency[949], latency[989], int64(after.HeapAlloc)-int64(before.HeapAlloc))
	// Cancellation removes queued work even though all workers remain blocked.
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		blocked.queue.mu.Lock()
		n := blocked.queue.pending
		blocked.queue.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled work retained capacity")
		}
		runtime.Gosched()
	}
}

func TestDisconnectRetainsSnapshotAndCloseWaitsForActualExit(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{Concurrency: 1})
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	c := connect(t, s, func(ctx context.Context, n int) (int, error) {
		if n == 0 {
			close(started)
			<-release
		}
		return n, ctx.Err()
	}, ConnectOptions{})
	first, _ := s.EmitAsync(context.Background(), 0)
	<-started
	second, _ := s.EmitAsync(context.Background(), 1)
	c.Disconnect()
	if r, err := s.Emit(context.Background(), 2); err != nil || len(r) != 0 {
		t.Fatal(r, err)
	}
	once.Do(func() { close(release) })
	for _, e := range []*Emission[int]{first, second} {
		if _, err := e.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("connection did not finish")
	}
}

func TestEmptySignalStillRejectsCanceledAndClosed(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Emit(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.BeginClose()
	if _, err := s.Emit(context.Background(), 0); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestConnectionCloseCancelsButWaitsForRunningCallback(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{})
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	c := connect(t, s, func(context.Context, int) (int, error) { close(started); <-release; return 1, nil }, ConnectOptions{})
	e, _ := s.EmitAsync(context.Background(), 0)
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := c.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if r, err := e.Wait(t.Context()); !errors.Is(err, context.Canceled) || !r[0].Started {
		t.Fatal(r, err)
	}
	select {
	case <-c.Done():
		t.Fatal("connection reported exit before callback")
	default:
	}
}

func TestPartialAdmissionKeepsEverySubscriberResult(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{Concurrency: 1, Capacity: 1})
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	if err := s.queue.Submit(context.Background(), Task{Run: func(context.Context) error { close(started); <-release; return nil }}); err != nil {
		t.Fatal(err)
	}
	<-started
	connect(t, s, func(context.Context, int) (int, error) { return 1, nil }, ConnectOptions{})
	connect(t, s, func(context.Context, int) (int, error) { return 2, nil }, ConnectOptions{})
	e, err := s.EmitAsync(context.Background(), 0)
	if !errors.Is(err, ErrQueueFull) || e == nil {
		t.Fatal(e, err)
	}
	once.Do(func() { close(release) })
	r, err := e.Wait(t.Context())
	if !errors.Is(err, ErrQueueFull) || len(r) != 2 || r[0].Value != 1 || !r[0].Started || !errors.Is(r[1].Err, ErrQueueFull) || r[1].Started {
		t.Fatal(r, err)
	}
}

func TestBlockedDiagnosticsNeverBlockSignalWorkers(t *testing.T) {
	if err := FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = w
	defer func() {
		os.Stderr = original
		_ = w.Close()
		_ = r.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := FlushDiagnostics(ctx); err != nil {
			t.Error(err)
		}
	}()
	for range 1000 {
		reportFailure(strings.Repeat("x", 8192))
	}
	s := testSignal[int, int](t, Options[int]{})
	want := errors.New("handler failed")
	connect(t, s, func(context.Context, int) (int, error) { return 4, want }, ConnectOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if results, err := s.Emit(ctx, 0); !errors.Is(err, want) || results[0].Value != 4 {
		t.Fatal(results, err)
	}
	if len(diagnostics) > cap(diagnostics) {
		t.Fatal("unbounded diagnostic backlog")
	}
}
