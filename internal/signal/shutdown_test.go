package signal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShutdownLifetimeMatrix(t *testing.T) {
	for _, lifetime := range []Lifetime{FollowEmit, FollowExecutor} {
		for _, policy := range []ShutdownPolicy{CancelPending, Drain} {
			for _, cancelEmit := range []bool{false, true} {
				t.Run(fmt.Sprintf("lifetime=%d/shutdown=%d/cancel=%t", lifetime, policy, cancelEmit), func(t *testing.T) {
					queue := testQueue(t, 2)
					started, stopping, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
					if err := queue.Submit(context.Background(), Task{Run: func(ctx context.Context) error {
						close(started)
						<-ctx.Done()
						close(stopping)
						<-release
						return ctx.Err()
					}}); err != nil {
						t.Fatal(err)
					}
					<-started
					var calls atomic.Int32
					s := New[int]("matrix")
					connect(t, s, func(context.Context, int) error { calls.Add(1); return nil }, ConnectOptions{Executor: queue, Lifetime: lifetime, Shutdown: policy})
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if err := s.Emit(ctx, 0); err != nil {
						t.Fatal(err)
					}
					if cancelEmit {
						cancel()
					}
					closed := make(chan error, 1)
					go func() { closed <- queue.Close(context.Background()) }()
					<-stopping // Close has applied the policies, but the worker is still blocked.
					close(release)
					if err := <-closed; err != nil {
						t.Fatal(err)
					}
					want := int32(0)
					if policy == Drain && (!cancelEmit || lifetime == FollowExecutor) {
						want = 1
					}
					if got := calls.Load(); got != want {
						t.Fatalf("calls=%d want=%d", got, want)
					}
				})
			}
		}
	}
}

func TestShutdownDeadlineCancelsEveryLifetimeAndPolicy(t *testing.T) {
	for _, lifetime := range []Lifetime{FollowEmit, FollowExecutor} {
		for _, policy := range []ShutdownPolicy{CancelPending, Drain} {
			t.Run(fmt.Sprintf("lifetime=%d/shutdown=%d", lifetime, policy), func(t *testing.T) {
				q := testQueue(t, 1)
				started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var pending atomic.Int32
				s := New[int]("deadline")
				connect(t, s, func(ctx context.Context, event int) error {
					if event != 0 {
						pending.Add(1)
						return nil
					}
					close(started)
					<-ctx.Done()
					close(cancelled)
					<-release
					return ctx.Err()
				}, ConnectOptions{Executor: q, Lifetime: lifetime, Shutdown: policy})
				if err := s.Emit(context.Background(), 0); err != nil {
					t.Fatal(err)
				}
				<-started
				if err := s.Emit(context.Background(), 1); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				if err := q.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Close = %v", err)
				}
				<-cancelled
				select {
				case <-q.Done():
					t.Fatal("Done closed before callback returned")
				default:
				}
				close(release)
				<-q.Done()
				if pending.Load() != 0 {
					t.Fatal("pending callback ran after shutdown deadline")
				}
			})
		}
	}
}

func TestMixedShutdownPoliciesKeepDrainOrder(t *testing.T) {
	q := testQueue(t, 4)
	started, stopping, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	if err := q.Submit(context.Background(), Task{Run: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(stopping)
		<-release
		return ctx.Err()
	}}); err != nil {
		t.Fatal(err)
	}
	<-started
	var calls []int
	for i, policy := range []ShutdownPolicy{Drain, CancelPending, Drain, CancelPending} {
		if err := q.Submit(context.Background(), Task{Shutdown: policy, Run: func(context.Context) error { calls = append(calls, i); return nil }}); err != nil {
			t.Fatal(err)
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- q.Close(context.Background()) }()
	<-stopping
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []int{0, 2}) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestSubmitRacesWithClose(t *testing.T) {
	q := testQueue(t, 256)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			<-start
			err := q.Submit(context.Background(), Task{Shutdown: Drain, Run: func(context.Context) error { return nil }})
			if err != nil && !errors.Is(err, ErrClosed) {
				t.Error(err)
			}
		})
	}
	wg.Go(func() {
		<-start
		if err := q.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	close(start)
	wg.Wait()
}

func TestCancellationLogsOnlyUnexpectedErrors(t *testing.T) {
	logs, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = logs
	t.Cleanup(func() { os.Stderr = oldStderr; _ = logs.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, failure := range []error{context.Canceled, fmt.Errorf("wrapped: %w", errors.Join(context.Canceled, errors.New("actual failure")))} {
		s := New[int]("logging")
		connect(t, s, func(context.Context, int) error { return failure }, ConnectOptions{})
		_ = s.Emit(ctx, 0)
	}
	data, err := os.ReadFile(logs.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); strings.Count(got, "level=ERROR") != 1 || !strings.Contains(got, "actual failure") {
		t.Fatalf("logs=%s", got)
	}
	queue, err := NewQueue(QueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	if err := queue.Submit(context.Background(), Task{Run: func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := queue.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := New[int]("closed")
	connect(t, s, func(context.Context, int) error { return nil }, ConnectOptions{Executor: queue, Lifetime: FollowExecutor})
	if err := s.Emit(context.Background(), 0); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(logs.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(data) {
		t.Fatalf("normal shutdown logged failure: %s", after)
	}
}

func TestDrainFailureDiagnosticDoesNotDependOnBackpressure(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = file
	t.Cleanup(func() { os.Stderr = old; _ = file.Close() })
	for _, policy := range []ShutdownPolicy{CancelPending, Drain} {
		queue, err := NewQueue(QueueOptions{Name: "generic", Capacity: 1})
		if err != nil {
			t.Fatal(err)
		}
		started, release := make(chan struct{}), make(chan struct{})
		if err := queue.Submit(context.Background(), Task{Shutdown: policy, Run: func(context.Context) error { close(started); <-release; return nil }}); err != nil {
			t.Fatal(err)
		}
		<-started
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := queue.Close(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		close(release)
		<-queue.Done()
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if policy == Drain {
			want = 1
		}
		if got := strings.Count(string(data), "signal drain incomplete"); got != want {
			t.Fatalf("policy=%d diagnostics=%s", policy, data)
		}
	}
}
