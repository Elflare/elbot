package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

func requireLifecycleError(t *testing.T, err error, component componentKind, phase lifecyclePhase) *lifecycleError {
	t.Helper()
	var detail *lifecycleError
	if !errors.As(err, &detail) || detail.component != component || detail.phase != phase {
		t.Fatalf("error=%v, want component=%v phase=%v", err, component, phase)
	}
	return detail
}

func TestLifecycleErrorPreservesNestedCauseAndCancellation(t *testing.T) {
	failure := errors.New("independent failure")
	for _, cause := range []error{context.Canceled, errors.Join(context.Canceled, failure)} {
		inner := &lifecycleError{component: componentMedia, phase: phaseBuild, cause: cause}
		outer := &lifecycleError{component: componentApp, phase: phaseResources, cause: inner}
		detail := requireLifecycleError(t, outer, componentApp, phaseResources)
		requireLifecycleError(t, errors.Unwrap(detail), componentMedia, phaseBuild)
		if !errors.Is(outer, context.Canceled) || errors.Is(outer, failure) != errors.Is(cause, failure) {
			t.Fatalf("lost cause: %v", outer)
		}
		if isOnlyCancellation(outer, context.Canceled) != (cause == context.Canceled) {
			t.Fatalf("incorrect cancellation classification: %v", outer)
		}
	}
}

func TestBusinessLinesKeepsRegistrationOrderForTies(t *testing.T) {
	var started []componentKind
	var lines businessLines
	for _, kind := range []componentKind{componentHook, componentTool, componentAgent} {
		lines.register(businessLine{kind: kind, startOrder: 1, supervisor: &testLifecycle{
			start: func() error { started = append(started, kind); return nil },
		}})
	}
	if err := startBusinessLines(t.Context(), lines); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(started, []componentKind{componentHook, componentTool, componentAgent}) {
		t.Fatalf("start order=%v", started)
	}
	for i, line := range lines {
		if line.kind != started[i] {
			t.Fatalf("registration order changed: %v", lines)
		}
	}
}

func TestBusinessLinesUsesRegisteredIngressForAnyComponent(t *testing.T) {
	var order []lifecyclePhase
	lines := businessLines{{
		kind: componentTool,
		supervisor: &testLifecycle{
			begin: func() { order = append(order, phaseBeginClose) },
			close: func(context.Context) error { order = append(order, phaseClose); return nil },
		},
		waitIngress: func(context.Context) error { order = append(order, phaseWaitIngress); return nil },
	}}
	if result := stopBusinessLines(t.Context(), lines); result.err != nil {
		t.Fatal(result.err)
	}
	if !slices.Equal(order, []lifecyclePhase{phaseBeginClose, phaseWaitIngress, phaseClose}) {
		t.Fatalf("phase order=%v", order)
	}
	if result := stopBusinessLines(t.Context(), nil); result.kind != stopCompleted || result.err != nil {
		t.Fatalf("empty registration=%+v", result)
	}
}

func TestStartupStepsRunInOrderAndMeasureCompletedStages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var order []lifecyclePhase
		timing := startupTiming{startedAt: time.Now(), stageStartedAt: time.Now()}
		var steps []startupStep
		for _, phase := range []lifecyclePhase{phaseEnvironment, phaseResources, phaseWiring, phaseStart} {
			steps = append(steps, startupStep{phase: phase, run: func() error {
				order = append(order, phase)
				time.Sleep(time.Second)
				return nil
			}})
		}
		if err := runStartupSteps(t.Context(), steps, &timing); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(order, []lifecyclePhase{phaseEnvironment, phaseResources, phaseWiring, phaseStart}) || len(timing.stages) != len(order) {
			t.Fatal(order, timing.stages)
		}
		for i, stage := range timing.stages {
			if stage.phase != order[i] || stage.duration != time.Second {
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
		{phase: phaseEnvironment, run: func() error { return nil }},
		{phase: phaseWiring, run: func() error {
			lines.register(businessLine{kind: componentAgent, supervisor: &testLifecycle{close: func(context.Context) error { closed = true; return nil }}})
			return failure
		}},
		{phase: phaseStart, run: func() error { later = true; return nil }},
	}, &timing)
	requireLifecycleError(t, err, componentApp, phaseWiring)
	if !errors.Is(err, failure) || later || len(timing.stages) != 1 {
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
				{phase: phaseEnvironment, run: func() error { calls++; cancel(); return nil }},
				{phase: phaseResources, run: func() error { calls++; return nil }},
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

func TestStartBusinessLinesSkipsMissingOwners(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			lines := businessLines{}
			started := false
			if partial {
				lines.register(businessLine{kind: componentHook, supervisor: &testLifecycle{start: func() error { started = true; return nil }}})
			}
			if err := startBusinessLines(t.Context(), lines); err != nil || started != partial {
				t.Fatalf("started=%v error=%v", started, err)
			}
		})
	}
}

func TestStartBusinessLinesUsesDependencyOrderAndStopsOnFailure(t *testing.T) {
	failure := errors.New("startup failed")
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			var order []componentKind
			newLine := func(kind componentKind) *testLifecycle {
				return &testLifecycle{start: func() error {
					order = append(order, kind)
					if fail && kind == componentAgent {
						return failure
					}
					return nil
				}}
			}
			lines := businessLines{
				{kind: componentBackground, supervisor: newLine(componentBackground), startOrder: 5},
				{kind: componentHook, supervisor: newLine(componentHook), startOrder: 3},
				{kind: componentTool, supervisor: newLine(componentTool), startOrder: 1},
				{kind: componentPlatform, supervisor: newLine(componentPlatform), startOrder: 4},
				{kind: componentAgent, supervisor: newLine(componentAgent), startOrder: 2},
			}
			registered := slices.Clone(lines)
			err := startBusinessLines(t.Context(), lines)
			for i := range lines {
				if lines[i].kind != registered[i].kind {
					t.Fatal("startup changed registration order")
				}
			}
			want := []componentKind{componentTool, componentAgent, componentHook, componentPlatform, componentBackground}
			if fail {
				want = want[:2]
				requireLifecycleError(t, err, componentAgent, phaseStart)
				if !errors.Is(err, failure) {
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

func TestWaitBusinessLinesDiscoversAvailableWaiters(t *testing.T) {
	for _, count := range []int{1, 3, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			lines := businessLines{}
			kinds := []componentKind{componentTool, componentAgent, componentHook, componentPlatform, componentBackground}
			if count < len(kinds) {
				lines.register(businessLine{kind: kinds[count], supervisor: &testLifecycle{}})
			}
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
				lines.register(businessLine{kind: kinds[i], supervisor: line})
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
		{kind: componentAgent, supervisor: &testWaitingLine{wait: func(context.Context) error { return first }}},
		{kind: componentBackground, supervisor: &testWaitingLine{wait: func(ctx context.Context) error {
			<-ctx.Done()
			return fmt.Errorf("combined: %w", errors.Join(ctx.Err(), second))
		}}},
	}
	err := waitBusinessLines(t.Context(), lines)
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("lost independent failure: %v", err)
	}
	joined := err.(interface{ Unwrap() []error }).Unwrap()
	if len(joined) != 2 {
		t.Fatalf("failures=%v", joined)
	}
	for _, failure := range joined {
		component := componentBackground
		if errors.Is(failure, first) {
			component = componentAgent
		}
		requireLifecycleError(t, failure, component, phaseWait)
	}
}

func TestWaitBusinessLinesExternalCancellationWithOrWithoutWaiters(t *testing.T) {
	for _, hasWaiter := range []bool{false, true} {
		t.Run(fmt.Sprint(hasWaiter), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lines := businessLines{{kind: componentTool, supervisor: &testLifecycle{}}}
				if hasWaiter {
					lines.register(businessLine{kind: componentBackground, supervisor: &testWaitingLine{wait: func(ctx context.Context) error {
						<-ctx.Done()
						return ctx.Err()
					}}})
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
	type identity struct {
		component componentKind
		phase     lifecyclePhase
	}
	var order []identity
	newLine := func(kind componentKind) *testLifecycle {
		return &testLifecycle{
			begin: func() { order = append(order, identity{kind, phaseBeginClose}) },
			close: func(context.Context) error { order = append(order, identity{kind, phaseClose}); return nil },
		}
	}
	lines := businessLines{
		{kind: componentTool, supervisor: newLine(componentTool), beginOrder: 5, closeOrder: 4},
		{kind: componentAgent, supervisor: newLine(componentAgent), beginOrder: 3, closeOrder: 2},
		{kind: componentHook, supervisor: newLine(componentHook), beginOrder: 4, closeOrder: 3},
		{kind: componentBackground, supervisor: newLine(componentBackground), beginOrder: 2, closeOrder: 1},
		{kind: componentPlatform, supervisor: newLine(componentPlatform), beginOrder: 1, closeOrder: 5, waitIngress: func(context.Context) error {
			order = append(order, identity{componentPlatform, phaseWaitIngress})
			return nil
		}},
	}
	if result := stopBusinessLines(t.Context(), lines); result.kind != stopCompleted {
		t.Fatal(result)
	}
	want := []identity{
		{componentPlatform, phaseBeginClose}, {componentBackground, phaseBeginClose},
		{componentAgent, phaseBeginClose}, {componentHook, phaseBeginClose}, {componentTool, phaseBeginClose},
		{componentPlatform, phaseWaitIngress},
		{componentBackground, phaseClose}, {componentAgent, phaseClose}, {componentHook, phaseClose},
		{componentTool, phaseClose}, {componentPlatform, phaseClose},
	}
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
			{kind: componentBackground, closeOrder: 1, supervisor: &testWaitingLine{
				testLifecycle: testLifecycle{close: func(context.Context) error { <-release; return nil }},
				wait:          func(context.Context) error { return failure },
			}},
			{kind: componentAgent, closeOrder: 2, supervisor: &testLifecycle{close: func(context.Context) error { closedDependency = true; return nil }}},
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
