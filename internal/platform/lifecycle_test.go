package platform

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLifecycle() *lifecycle {
	l := &lifecycle{}
	l.init(context.Background(), "service")
	return l
}

func testRuntime(name string, critical bool, result error) (*platformLifecycle, chan struct{}) {
	done := make(chan struct{})
	var once sync.Once
	return &platformLifecycle{name: name, stopApp: critical,
		start:      func(context.Context) error { return nil },
		beginClose: func() { once.Do(func() { close(done) }) },
		wait: func(ctx context.Context) error {
			select {
			case <-done:
				return result
			default:
			}
			select {
			case <-done:
				return result
			case <-ctx.Done():
				return ctx.Err()
			}
		}}, done
}

func TestServiceExitDoesNotStopLine(t *testing.T) {
	l := testLifecycle()
	r, _ := testRuntime("remote", false, errors.New("offline"))
	l.runtimes = []*platformLifecycle{r}
	reported := make(chan struct{})
	l.onFailure = func(string, error) { close(reported) }
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	r.beginClose()
	<-reported
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx); err != context.Canceled {
		t.Fatalf("Wait = %v", err)
	}
	if l.ctx.Err() != nil {
		t.Fatal("canceling observation stopped service")
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTUIExitStopsIngressAndPreservesResources(t *testing.T) {
	for _, fault := range []error{nil, errors.New("terminal failed")} {
		t.Run(fmtTestError(fault), func(t *testing.T) {
			l := testLifecycle()
			r, _ := testRuntime("cli", true, fault)
			l.runtimes = []*platformLifecycle{r}
			var closed atomic.Bool
			l.closeSteps = []closeStep{{"output/history", func(context.Context) error { closed.Store(true); return nil }}}
			if err := l.Start(); err != nil {
				t.Fatal(err)
			}
			r.beginClose()
			if err := l.Wait(context.Background()); !errors.Is(err, fault) {
				t.Fatalf("Wait = %v", err)
			}
			if closed.Load() {
				t.Fatal("ingress exit closed output/history")
			}
			select {
			case <-l.Done():
				t.Fatal("Done before final Close")
			default:
			}
			if err := l.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !closed.Load() {
				t.Fatal("resources not closed")
			}
		})
	}
}

func fmtTestError(err error) string {
	if err == nil {
		return "normal"
	}
	return "fault"
}

func TestConcurrentStartAndRepeatedClose(t *testing.T) {
	l := testLifecycle()
	r, _ := testRuntime("remote", false, nil)
	var starts, closes atomic.Int32
	r.start = func(context.Context) error { starts.Add(1); return nil }
	l.runtimes = []*platformLifecycle{r}
	l.closeSteps = []closeStep{{"resources", func(context.Context) error { closes.Add(1); return nil }}}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if err := l.Start(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for range 10 {
		wg.Go(func() {
			if err := l.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if starts.Load() != 1 || closes.Load() != 1 {
		t.Fatalf("starts=%d closes=%d", starts.Load(), closes.Load())
	}
}

func TestPartialStartWaitsForActualExit(t *testing.T) {
	l := testLifecycle()
	fault := errors.New("start failed")
	exit := make(chan struct{})
	r, _ := testRuntime("partial", false, nil)
	r.start = func(context.Context) error { return fault }
	r.beginClose = func() {}
	r.wait = func(context.Context) error { <-exit; return nil }
	l.runtimes = []*platformLifecycle{r}
	if err := l.Start(); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Close(ctx); err != context.Canceled {
		t.Fatalf("Close = %v", err)
	}
	select {
	case <-l.Done():
		t.Fatal("closed while ingress remained")
	default:
	}
	close(exit)
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := l.Wait(context.Background()); !errors.Is(err, fault) {
		t.Fatal(err)
	}
}

func TestCloseDeadlineRetainsDependenciesAndCanResume(t *testing.T) {
	l := testLifecycle()
	var order []string
	ctx, cancel := context.WithCancel(context.Background())
	first := true
	l.closeSteps = []closeStep{
		{"adapter", func(context.Context) error {
			if first {
				first = false
				cancel()
				return context.Canceled
			}
			order = append(order, "adapter")
			return nil
		}},
		{"history", func(context.Context) error { order = append(order, "history"); return nil }},
	}
	if err := l.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(order) != 0 {
		t.Fatal("released dependencies after deadline")
	}
	select {
	case <-l.Done():
		t.Fatal("Done on timeout")
	default:
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"adapter", "history"}) {
		t.Fatal(order)
	}
	select {
	case <-l.Done():
	default:
		t.Fatal("Done not closed")
	}
	if err := l.Start(); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestCanceledWaitReadsPublishedFaultBeforeObserver(t *testing.T) {
	l := testLifecycle()
	fault := errors.Join(context.Canceled, errors.New("independent failure"))
	observerEntered, release := make(chan struct{}), make(chan struct{})
	r, _ := testRuntime("cli", true, nil)
	r.wait = func(ctx context.Context) error {
		if ctx.Err() != nil {
			return fault
		}
		close(observerEntered)
		<-release
		return fault
	}
	l.runtimes = []*platformLifecycle{r}
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	<-observerEntered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx); !errors.Is(err, fault) {
		t.Fatalf("lost published fault: %v", err)
	}
	close(release)
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUnstartedPartialConstructionAndCleanupFailure(t *testing.T) {
	l := testLifecycle()
	fault := errors.New("cleanup failed")
	var historyClosed bool
	l.closeSteps = []closeStep{
		{"partial adapter", func(context.Context) error { return fault }},
		{"history", func(context.Context) error { historyClosed = true; return nil }},
	}
	if err := l.Close(context.Background()); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	if !historyClosed {
		t.Fatal("ordinary failure skipped history cleanup")
	}
	if err := l.Close(context.Background()); !errors.Is(err, fault) {
		t.Fatal(err)
	}
}

func TestParentCancellationStopsIngress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l := &lifecycle{}
	l.init(ctx, "service")
	r, _ := testRuntime("remote", false, nil)
	l.runtimes = []*platformLifecycle{r}
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	wait, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := l.Wait(wait); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(wait); err != nil {
		t.Fatal(err)
	}
}

func TestCompletedWaitWinsOverCanceledObservation(t *testing.T) {
	l := testLifecycle()
	l.BeginClose()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx); err != nil {
		t.Fatalf("completed Wait = %v", err)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
