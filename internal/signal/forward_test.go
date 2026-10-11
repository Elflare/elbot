package signal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestForwardResolvedPreservesSingleResult(t *testing.T) {
	failure := errors.New("partial delivery")
	for _, want := range []Result[int]{{}, {Err: ErrClosed}, {Value: 42, Err: failure, Started: true}} {
		s := testSignal[int, int](t, Options[int]{Single: true})
		_, err := s.ConnectForward(func(context.Context, int) *Emission[int] { return Resolved(want) }, ConnectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Emit(context.Background(), 0)
		if len(got) != 1 || got[0] != want || !errors.Is(err, want.Err) {
			t.Fatalf("got=%+v err=%v want=%+v", got, err, want)
		}
	}
}

func TestForwardNilIsContractError(t *testing.T) {
	s := testSignal[int, int](t, Options[int]{Single: true})
	_, err := s.ConnectForward(func(context.Context, int) *Emission[int] { return nil }, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	results, err := s.Emit(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "nil forwarded emission") || results[0].Started {
		t.Fatal(results, err)
	}
}

func TestForwardRejectedEmissionKeepsAdmissionErrorOnce(t *testing.T) {
	down := testSignal[int, int](t, Options[int]{Single: true, Concurrency: 1, Capacity: 1})
	started, release := make(chan struct{}, 1), make(chan struct{})
	defer close(release)
	connect(t, down, func(context.Context, int) (int, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return 0, nil
	}, ConnectOptions{})
	_, _ = down.EmitAsync(context.Background(), 0)
	<-started
	_, _ = down.EmitAsync(context.Background(), 0)
	up := testSignal[int, int](t, Options[int]{Single: true})
	_, err := up.ConnectForward(func(ctx context.Context, n int) *Emission[int] {
		e, admission := down.EmitAsync(ctx, n)
		if e == nil {
			return Resolved(Result[int]{Err: admission})
		}
		return e
	}, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	results, err := up.Emit(context.Background(), 1)
	if !errors.Is(err, ErrQueueFull) || len(results) != 1 || results[0].Err != ErrQueueFull || results[0].Started {
		t.Fatal(results, err)
	}
}
