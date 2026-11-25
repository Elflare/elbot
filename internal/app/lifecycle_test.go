package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"

	"elbot/internal/events"
	"elbot/internal/signal"
)

type testLifecycle struct {
	start func() error
	begin func()
	close func(context.Context) error
	done  <-chan struct{}
}

func (l *testLifecycle) Start() error {
	if l.start != nil {
		return l.start()
	}
	return nil
}

func (l *testLifecycle) BeginClose() {
	if l.begin != nil {
		l.begin()
	}
}

func (l *testLifecycle) Close(ctx context.Context) error {
	if l.close != nil {
		return l.close(ctx)
	}
	return nil
}

func (l *testLifecycle) Done() <-chan struct{} {
	if l.done != nil {
		return l.done
	}
	done := make(chan struct{})
	close(done)
	return done
}

type testWaitingLine struct {
	testLifecycle
	wait func(context.Context) error
}

func (l *testWaitingLine) Wait(ctx context.Context) error { return l.wait(ctx) }

func testBusinessLines() businessLines {
	lines := businessLines{active: make(map[componentKind]businessLine)}
	for _, kind := range []componentKind{componentTool, componentAgent, componentHook, componentPlatform, componentBackground} {
		lines.active[kind] = businessLine{supervisor: &testLifecycle{}}
	}
	lines.active[componentPlatform] = businessLine{supervisor: &testLifecycle{}, waitIngress: func(context.Context) error { return nil }}
	return lines
}

func TestStartBusinessLinesUsesDependencyOrderAndStopsOnFailure(t *testing.T) {
	failure := errors.New("startup failed")
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			var order []componentKind
			lines := testBusinessLines()
			for kind, line := range lines.active {
				line.supervisor.(*testLifecycle).start = func() error {
					order = append(order, kind)
					if fail && kind == componentAgent {
						return failure
					}
					return nil
				}
			}
			err := startBusinessLines(t.Context(), lines)
			want := []componentKind{componentTool, componentAgent, componentHook, componentPlatform, componentBackground}
			if fail {
				want = want[:2]
				if !errors.Is(err, failure) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(order, want) {
				t.Fatalf("start order=%v, want %v", order, want)
			}
		})
	}
}

func TestWaitBusinessLinesJoinsPlatformAndBackground(t *testing.T) {
	joined := make(chan struct{})
	lines := testBusinessLines()
	lines.active[componentPlatform] = businessLine{supervisor: &testWaitingLine{wait: func(context.Context) error { return nil }}}
	lines.active[componentBackground] = businessLine{supervisor: &testWaitingLine{wait: func(ctx context.Context) error {
		<-ctx.Done()
		close(joined)
		return fmt.Errorf("wait canceled: %w", ctx.Err())
	}}}
	if err := waitBusinessLines(t.Context(), lines); err != nil {
		t.Fatal(err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("background observer still running")
	}
}

func TestWaitBusinessLinesPreservesConcurrentIndependentFailures(t *testing.T) {
	first, second := errors.New("first fault"), errors.New("second fault")
	records := make(chan events.LogRecord, 1)
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) { records <- record }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	lines := testBusinessLines()
	lines.active[componentPlatform] = businessLine{supervisor: &testWaitingLine{wait: func(context.Context) error { return first }}}
	lines.active[componentBackground] = businessLine{supervisor: &testWaitingLine{wait: func(ctx context.Context) error {
		<-ctx.Done()
		return second
	}}}
	err = waitBusinessLines(t.Context(), lines)
	if !errors.Is(err, first) || errors.Is(err, second) {
		t.Fatalf("runtime error=%v", err)
	}
	if record := <-records; record.Content != "background wait: second fault" {
		t.Fatalf("independent failure log=%+v", record)
	}
}

func TestWaitBusinessLinesExternalCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lines := testBusinessLines()
		for _, kind := range []componentKind{componentPlatform, componentBackground} {
			lines.active[kind] = businessLine{supervisor: &testWaitingLine{wait: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			}}}
		}
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() { result <- waitBusinessLines(ctx, lines) }()
		synctest.Wait()
		select {
		case <-result:
			t.Fatal("returned before cancellation")
		default:
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}
