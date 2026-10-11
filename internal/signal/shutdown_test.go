package signal

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

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
		if err := FlushDiagnostics(t.Context()); err != nil {
			t.Fatal(err)
		}
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
