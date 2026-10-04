package signal

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func connect[T any](t *testing.T, s *Signal[T], handler Handler[T], options ConnectOptions) *Connection {
	t.Helper()
	c, err := s.Connect(handler, options)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSnapshotReentrancyAndErrors(t *testing.T) {
	s := New[int]("reentrant", testLogger())
	wantErr := errors.New("failed")
	var calls []int
	var second *Connection
	connect(t, s, func(ctx context.Context, n int) error {
		calls = append(calls, 10+n)
		second.Disconnect()
		connect(t, s, func(_ context.Context, n int) error { calls = append(calls, 30+n); return nil }, ConnectOptions{})
		if err := s.Emit(ctx, n+1); err != nil {
			t.Error(err)
		}
		return wantErr
	}, ConnectOptions{Once: true})
	second = connect(t, s, func(_ context.Context, n int) error { calls = append(calls, 20+n); return nil }, ConnectOptions{})
	if err := s.Emit(context.Background(), 0); !errors.Is(err, wantErr) {
		t.Fatalf("Emit = %v", err)
	}
	if want := []int{10, 31, 20}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v; want %v", calls, want)
	}
	second.Disconnect()
	if err := s.Emit(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if calls[len(calls)-1] != 32 {
		t.Fatal(calls)
	}
}

func TestConcurrentOnce(t *testing.T) {
	s := New[int]("once", testLogger())
	var count atomic.Int32
	connect(t, s, func(ctx context.Context, n int) error { count.Add(1); return s.Emit(ctx, n+1) }, ConnectOptions{Once: true})
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if err := s.Emit(context.Background(), 0); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := count.Load(); got != 1 {
		t.Fatalf("calls = %d", got)
	}
}

type rejectExecutor struct{ calls int }

func (e *rejectExecutor) Submit(context.Context, Task) error {
	e.calls++
	return ErrQueueFull
}

func TestOnceSubmissionFailureIsConsumedAndOtherHandlersRun(t *testing.T) {
	s := New[int]("full", testLogger())
	executor := &rejectExecutor{}
	calls := 0
	connect(t, s, func(context.Context, int) error { t.Fatal("unexpected execution"); return nil }, ConnectOptions{Executor: executor, Lifetime: FollowEmit, Once: true})
	connect(t, s, func(context.Context, int) error { calls++; return nil }, ConnectOptions{})
	if err := s.Emit(context.Background(), 0); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	if err := s.Emit(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || calls != 2 {
		t.Fatalf("submissions=%d calls=%d", executor.calls, calls)
	}
}

func TestConnectValidation(t *testing.T) {
	s := New[int]("validation", nil)
	handler := func(context.Context, int) error { return nil }
	if _, err := s.Connect(nil, ConnectOptions{}); err == nil {
		t.Fatal("nil handler accepted")
	}
	for _, options := range []ConnectOptions{{Lifetime: FollowEmit}, {Executor: &rejectExecutor{}}, {Executor: &rejectExecutor{}, Lifetime: 99}} {
		if _, err := s.Connect(handler, options); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
}
