package signal

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestForwardPreservesBusinessResult(t *testing.T) {
	failure := errors.New("partial delivery")
	up := testSignal[int, int](t, Options[int]{Single: true})
	down := testSignal[int, int](t, Options[int]{Single: true})
	connect(t, down, func(context.Context, int) (int, error) { return 42, failure }, ConnectOptions{})
	_, err := up.ConnectForward(func(ctx context.Context, n int) (context.Context, *Signal[int, int], int) { return ctx, down, n }, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := up.Emit(t.Context(), 1)
	if err != nil || len(got) != 1 || got[0].Value != 42 || got[0].Err != failure || !got[0].Started {
		t.Fatal(got, err)
	}
}

func TestForwardRejectsNonSingleDestinationBeforeDispatch(t *testing.T) {
	up := testSignal[int, int](t, Options[int]{Single: true})
	down := testSignal[int, int](t, Options[int]{})
	var calls atomic.Int32
	for range 2 {
		connect(t, down, func(context.Context, int) (int, error) { calls.Add(1); return 1, nil }, ConnectOptions{})
	}
	_, err := up.ConnectForward(func(ctx context.Context, n int) (context.Context, *Signal[int, int], int) { return ctx, down, n }, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	results, err := up.Emit(t.Context(), 0)
	if err != nil || len(results) != 1 || results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "single-receiver") {
		t.Fatal(results, err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid destination executed business callbacks")
	}
}

func TestForwardEmptyDestinationHonorsRequiredOption(t *testing.T) {
	for _, required := range []bool{false, true} {
		up := testSignal[int, int](t, Options[int]{Single: true})
		down := testSignal[int, int](t, Options[int]{Single: true, Required: required})
		_, err := up.ConnectForward(func(ctx context.Context, n int) (context.Context, *Signal[int, int], int) { return ctx, down, n }, ConnectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		results, err := up.Emit(t.Context(), 0)
		if err != nil || len(results) != 1 || results[0].Started {
			t.Fatal(results, err)
		}
		if required && !errors.Is(results[0].Err, ErrNoReceiver) {
			t.Fatal(results)
		}
		if !required && results[0].Err != nil {
			t.Fatal(results)
		}
	}
}

func TestForwardCloseWaitsForActualDownstreamExit(t *testing.T) {
	up := testSignal[int, Empty](t, Options[int]{Single: true})
	down := testSignal[int, Empty](t, Options[int]{Single: true})
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	_, err := down.Observe(func(ctx context.Context, _ int) { close(started); <-ctx.Done(); close(canceled); <-release }, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = up.ConnectForward(func(ctx context.Context, n int) (context.Context, *Signal[int, Empty], int) { return ctx, down, n }, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	up.EmitAsync(t.Context(), 0)
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := up.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("upstream close did not cancel downstream")
	}
	select {
	case <-up.Done():
		t.Fatal("Done before downstream exit")
	default:
	}
}

func TestForwardNilDestinationSkipsDelivery(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{Single: true})
	_, err := s.ConnectForward(func(ctx context.Context, n int) (context.Context, *Signal[int, int], int) { return ctx, nil, n }, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Emit(t.Context(), 0)
	if err != nil || len(got) != 1 || got[0].Err != nil || got[0].Started {
		t.Fatal(got, err)
	}
}

func TestForwardReturnsAdmissionError(t *testing.T) {
	down := testSignal[int, int](t, Options[int]{Single: true, Concurrency: 1, Capacity: 1})
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	connect(t, down, func(context.Context, int) (int, error) { close(started); <-release; return 0, nil }, ConnectOptions{})
	_, _ = down.emit(t.Context(), 0, true)
	<-started
	_, _ = down.emit(t.Context(), 0, true)
	up := testSignal[int, int](t, Options[int]{Single: true})
	_, err := up.ConnectForward(func(ctx context.Context, n int) (context.Context, *Signal[int, int], int) { return ctx, down, n }, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := up.Emit(t.Context(), 1)
	if err != nil || len(got) != 1 || got[0].Err != ErrQueueFull || got[0].Started {
		t.Fatal(got, err)
	}
}
