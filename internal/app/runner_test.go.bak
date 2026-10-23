package app

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	platformmodule "elbot/internal/platform/module"
)

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

type lifecycleFunc func(context.Context) error

func (f lifecycleFunc) Close(ctx context.Context) error { return f(ctx) }

type profilerStub struct{ events *[]string }

func (profilerStub) SetEnabled(bool) {}
func (profilerStub) Mark(string)     {}
func (p profilerStub) Flush() time.Duration {
	if p.events != nil {
		*p.events = append(*p.events, "flush")
	}
	return time.Second
}

type environmentStub struct {
	events    *[]string
	mode      RunMode
	profiler  StartupProfiler
	markerErr error
}

func (s environmentStub) ResolveMode(RunMode) (RunMode, error) {
	*s.events = append(*s.events, "resolve")
	return s.mode, nil
}
func (s environmentStub) ClaimServiceMarker(RunMode) (io.Closer, error) {
	*s.events = append(*s.events, "marker")
	return closeFunc(func() error { *s.events = append(*s.events, "marker-close"); return s.markerErr }), nil
}
func (s environmentStub) NewStartupProfiler(time.Time) StartupProfiler {
	*s.events = append(*s.events, "profiler")
	return s.profiler
}

type foundationFactoryFunc func(context.Context, FoundationRequest) (*FoundationComponents, error)

func (f foundationFactoryFunc) Build(ctx context.Context, req FoundationRequest) (*FoundationComponents, error) {
	return f(ctx, req)
}

type modelFactoryFunc func(ModelRequest) (ModelClients, error)

func (f modelFactoryFunc) Build(req ModelRequest) (ModelClients, error) { return f(req) }

type platformFactoryFunc func(context.Context, PlatformRequest) (*platformmodule.Module, error)

func (f platformFactoryFunc) Build(ctx context.Context, req PlatformRequest) (*platformmodule.Module, error) {
	return f(ctx, req)
}

type runtimeFactoryFunc func(context.Context, WiringRequest) (*WiringComponents, error)

func (f runtimeFactoryFunc) Build(ctx context.Context, req WiringRequest) (*WiringComponents, error) {
	return f(ctx, req)
}

type moduleLifecycleStub struct {
	done  chan struct{}
	begin func()
	close func(context.Context) error
}

func (s *moduleLifecycleStub) BeginClose() {
	if s.begin != nil {
		s.begin()
	}
}
func (s *moduleLifecycleStub) Done() <-chan struct{}           { return s.done }
func (s *moduleLifecycleStub) Close(ctx context.Context) error { return s.close(ctx) }
func TestRunnerPartialConstructionUsesSharedBudgetAndReverseCleanup(t *testing.T) {
	var events []string
	failure := errors.New("construction failed")
	closeFailure := errors.New("close failed")
	var deadline time.Time
	d := DefaultDependencies()
	d.Environment = environmentStub{events: &events, mode: RunModeCLIOnly, profiler: profilerStub{events: &events}}
	d.Foundation = foundationFactoryFunc(func(context.Context, FoundationRequest) (*FoundationComponents, error) {
		return &FoundationComponents{Lifecycle: lifecycleFunc(func(ctx context.Context) error {
			events = append(events, "foundation-close")
			end, _ := ctx.Deadline()
			if !end.Equal(deadline) {
				t.Error("shutdown budget restarted")
			}
			return nil
		})}, nil
	})
	d.Models = modelFactoryFunc(func(ModelRequest) (ModelClients, error) { return ModelClients{}, nil })
	d.Wiring = runtimeFactoryFunc(func(context.Context, WiringRequest) (*WiringComponents, error) {
		s := &moduleLifecycleStub{done: make(chan struct{}), begin: func() { events = append(events, "begin") }}
		s.close = func(ctx context.Context) error {
			deadline, _ = ctx.Deadline()
			events = append(events, "module-close")
			close(s.done)
			return closeFailure
		}
		return &WiringComponents{Lifecycle: s}, failure
	})
	r := NewRunner(d)
	err := r.Run(t.Context(), Options{})
	if !errors.Is(err, failure) || !errors.Is(err, closeFailure) {
		t.Fatal(err)
	}
	if got := events[len(events)-4:]; !reflect.DeepEqual(got, []string{"begin", "module-close", "foundation-close", "marker-close"}) {
		t.Fatal(events)
	}
}
func TestRunnerTimeoutKeepsLiveModuleDependencies(t *testing.T) {
	var events []string
	failure := errors.New("construction failed")
	done := make(chan struct{})
	d := DefaultDependencies()
	d.Environment = environmentStub{events: &events, profiler: profilerStub{}}
	foundationClosed := false
	d.Foundation = foundationFactoryFunc(func(context.Context, FoundationRequest) (*FoundationComponents, error) {
		return &FoundationComponents{Lifecycle: lifecycleFunc(func(context.Context) error { foundationClosed = true; return nil })}, nil
	})
	d.Models = modelFactoryFunc(func(ModelRequest) (ModelClients, error) { return ModelClients{}, nil })
	d.Wiring = runtimeFactoryFunc(func(context.Context, WiringRequest) (*WiringComponents, error) {
		return &WiringComponents{Lifecycle: &moduleLifecycleStub{done: done, close: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}}, failure
	})
	r := NewRunner(d)
	r.shutdownTimeout = 10 * time.Millisecond
	if err := r.Run(t.Context(), Options{}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if foundationClosed {
		t.Fatal("borrowed dependencies closed while module is alive")
	}
	close(done)
	if foundationClosed {
		t.Fatal("a cleanup chain continued after timeout")
	}
}
func TestExpectedShutdownFilteringPreservesJoinedBusinessFailure(t *testing.T) {
	failure := errors.New("business failure")
	if got := withoutShutdownError(errors.Join(context.Canceled, failure), context.Canceled); !errors.Is(got, failure) || errors.Is(got, context.Canceled) {
		t.Fatal(got)
	}
}
