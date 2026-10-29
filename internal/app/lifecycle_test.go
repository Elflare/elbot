package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type testLifecycle struct {
	start func() error
	begin func()
	close func(context.Context) error
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

type testWaitingLine struct {
	testLifecycle
	wait func(context.Context) error
}

func (l *testWaitingLine) Wait(ctx context.Context) error { return l.wait(ctx) }

type testIngressLine struct {
	testLifecycle
	waitIngress func(context.Context) error
}

func (l *testIngressLine) WaitIngress(ctx context.Context) error { return l.waitIngress(ctx) }

func TestStartupStepsRunInOrderAndMeasureCompletedStages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var order []string
		timing := startupTiming{startedAt: time.Now(), stageStartedAt: time.Now()}
		var steps []startupStep
		for _, name := range []string{"environment", "resources", "wiring", "start", "future"} {
			steps = append(steps, startupStep{name: name, run: func() error {
				order = append(order, name)
				time.Sleep(time.Second)
				return nil
			}})
		}
		if err := runStartupSteps(t.Context(), steps, &timing); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(order, []string{"environment", "resources", "wiring", "start", "future"}) || len(timing.stages) != len(order) {
			t.Fatal(order, timing.stages)
		}
		for i, stage := range timing.stages {
			if stage.name != order[i] || stage.duration != time.Second {
				t.Fatalf("stage timing = %#v", stage)
			}
		}
	})
}

func TestStartupFailurePreservesPartialOwnershipForShutdown(t *testing.T) {
	failure := errors.New("partial construction")
	lines := businessLines{}
	closed, later := false, false
	timing := startupTiming{stageStartedAt: time.Now()}
	err := runStartupSteps(t.Context(), []startupStep{
		{name: "environment", run: func() error { return nil }},
		{name: "wiring", run: func() error {
			owner := &testLifecycle{close: func(context.Context) error { closed = true; return nil }}
			return register(lines, "agent", 2, owner, failure)
		}},
		{name: "start", run: func() error { later = true; return nil }},
	}, &timing)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "wiring") || later || len(timing.stages) != 1 {
		t.Fatalf("err=%v later=%v timing=%v", err, later, timing.stages)
	}
	if result := stopBusinessLines(t.Context(), lines); result.err != nil || !closed {
		t.Fatalf("partial cleanup=%+v closed=%v", result, closed)
	}
}

func TestStartupCancellationStopsFollowingStages(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprint(early), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if early {
				cancel()
			}
			calls := 0
			timing := startupTiming{stageStartedAt: time.Now()}
			err := runStartupSteps(ctx, []startupStep{
				{name: "one", run: func() error { calls++; cancel(); return nil }},
				{name: "two", run: func() error { calls++; return nil }},
			}, &timing)
			want := 1
			if early {
				want = 0
			}
			if !errors.Is(err, context.Canceled) || calls != want || len(timing.stages) != want {
				t.Fatalf("err=%v calls=%d timing=%v", err, calls, timing.stages)
			}
		})
	}
}

func TestRegisterPreservesPartialOwnershipAndSkipsNil(t *testing.T) {
	lines := businessLines{}
	failure := errors.New("constructor failed")
	line := &testLifecycle{}
	if err := register(lines, "partial", 7, line, failure); !errors.Is(err, failure) || !strings.Contains(err.Error(), "partial") {
		t.Fatal(err)
	}
	if lines["partial"].supervisor != line || lines["partial"].startOrder != 7 {
		t.Fatalf("partial ownership lost: %#v", lines)
	}
	var absent *testLifecycle
	if err := register(lines, "absent", 8, absent, failure); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, ok := lines["absent"]; ok {
		t.Fatal("typed nil entered lifecycle collection")
	}
}

func TestStartBusinessLinesUsesRegisteredOrderAndStopsOnFailure(t *testing.T) {
	failure := errors.New("startup failed")
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			lines := businessLines{}
			var order []string
			for _, name := range []string{"new-worker", "storage-worker", "another-worker"} {
				priority := map[string]int{"new-worker": 3, "storage-worker": 1, "another-worker": 2}[name]
				line := &testLifecycle{start: func() error {
					order = append(order, name)
					if fail && name == "another-worker" {
						return failure
					}
					return nil
				}}
				if err := register(lines, name, priority, line, nil); err != nil {
					t.Fatal(err)
				}
			}
			err := startBusinessLines(t.Context(), lines)
			want := []string{"storage-worker", "another-worker", "new-worker"}
			if fail {
				want = want[:2]
				if !errors.Is(err, failure) || !strings.Contains(err.Error(), "another-worker") {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(order, want) {
				t.Fatalf("start order = %v", order)
			}
		})
	}
}

func TestWaitBusinessLinesDiscoversAnyNumberOfWaiters(t *testing.T) {
	for _, count := range []int{1, 3, 7} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			lines := businessLines{"passive": {supervisor: &testLifecycle{}}}
			entered := make(chan struct{}, count)
			release := make(chan struct{})
			var exited atomic.Int32
			for i := range count {
				line := &testWaitingLine{wait: func(ctx context.Context) error {
					defer exited.Add(1)
					entered <- struct{}{}
					if i == 0 {
						<-release
						return nil
					}
					<-ctx.Done()
					return fmt.Errorf("wait canceled: %w", ctx.Err())
				}}
				if err := register(lines, fmt.Sprintf("dynamic-%d", i), i, line, nil); err != nil {
					t.Fatal(err)
				}
			}
			result := make(chan error, 1)
			go func() { result <- waitBusinessLines(t.Context(), lines) }()
			for range count {
				<-entered
			}
			close(release)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			if exited.Load() != int32(count) {
				t.Fatalf("waiters still running: exited=%d", exited.Load())
			}
		})
	}
}

func TestWaitBusinessLinesPreservesConcurrentIndependentFailures(t *testing.T) {
	first, second := errors.New("first fault"), errors.New("second fault")
	lines := businessLines{
		"one": {supervisor: &testWaitingLine{wait: func(context.Context) error { return first }}},
		"two": {supervisor: &testWaitingLine{wait: func(ctx context.Context) error {
			<-ctx.Done()
			return fmt.Errorf("combined: %w", errors.Join(ctx.Err(), second))
		}}},
	}
	err := waitBusinessLines(t.Context(), lines)
	if !errors.Is(err, first) || !errors.Is(err, second) || !strings.Contains(err.Error(), "one") || !strings.Contains(err.Error(), "two") {
		t.Fatalf("lost independent failure: %v", err)
	}
}

func TestWaitBusinessLinesExternalCancellationWithOrWithoutWaiters(t *testing.T) {
	for _, hasWaiter := range []bool{false, true} {
		t.Run(fmt.Sprint(hasWaiter), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lines := businessLines{"passive": {supervisor: &testLifecycle{}}}
				if hasWaiter {
					lines["active"] = businessLine{supervisor: &testWaitingLine{wait: func(ctx context.Context) error {
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
		})
	}
}

func TestStopBusinessLinesPreservesIngressAndDependencyPhases(t *testing.T) {
	var order []string
	lines := businessLines{}
	for _, name := range []string{"platform", "background", "agent", "hook", "tool"} {
		lifecycle := testLifecycle{
			begin: func() { order = append(order, "begin "+name) },
			close: func(context.Context) error { order = append(order, "close "+name); return nil },
		}
		var owner Lifecycle = &lifecycle
		if name == "platform" {
			owner = &testIngressLine{testLifecycle: lifecycle, waitIngress: func(context.Context) error {
				order = append(order, "wait ingress")
				return nil
			}}
		}
		lines[name] = businessLine{supervisor: owner}
	}
	if result := stopBusinessLines(t.Context(), lines); result.kind != stopCompleted {
		t.Fatal(result)
	}
	want := []string{"begin platform", "begin background", "begin agent", "begin hook", "begin tool", "wait ingress", "close background", "close agent", "close hook", "close tool", "close platform"}
	if !slices.Equal(order, want) {
		t.Fatalf("shutdown order = %v", order)
	}
}

func TestRunFailureDoesNotWaitForCleanupBeforeShutdownBudgetStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("runtime fault")
		release := make(chan struct{})
		defer close(release)
		closedDependency := false
		lines := businessLines{
			"background": {supervisor: &testWaitingLine{
				testLifecycle: testLifecycle{close: func(context.Context) error { <-release; return nil }},
				wait:          func(context.Context) error { return failure },
			}},
			"agent": {supervisor: &testLifecycle{close: func(context.Context) error { closedDependency = true; return nil }}},
		}
		start := time.Now()
		if err := waitBusinessLines(t.Context(), lines); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		if time.Since(start) != 0 {
			t.Fatal("runtime observation waited for cleanup")
		}
		if err := shutdown(t.Context(), lines, nil, nil); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if time.Since(start) != shutdownTimeout || closedDependency {
			t.Fatalf("deadline=%s dependencyClosed=%v", time.Since(start), closedDependency)
		}
	})
}
