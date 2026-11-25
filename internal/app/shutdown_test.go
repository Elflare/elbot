package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/events"
	"elbot/internal/logging"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func TestShutdownStepDeadlineDoesNotWaitForUncooperativeCloser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		release := make(chan struct{})
		defer close(release)
		result := runShutdownStep(ctx, shutdownStep{component: componentMedia, phase: phaseClose, run: func(context.Context) error { <-release; return nil }})
		if result.kind != stopTimedOut || !errors.Is(result.err, context.DeadlineExceeded) {
			t.Fatalf("result=%+v", result)
		}
		called := false
		result = runShutdownStep(ctx, shutdownStep{component: componentStorage, phase: phaseClose, run: func(context.Context) error { called = true; return nil }})
		if result.kind != stopTimedOut || called {
			t.Fatalf("started a dependency after deadline: result=%+v called=%v", result, called)
		}
	})
}

func TestShutdownStepPreservesTypedCause(t *testing.T) {
	for _, cause := range []error{errors.New("close failed"), context.DeadlineExceeded} {
		result := runShutdownStep(t.Context(), shutdownStep{component: componentModels, phase: phaseClose, run: func(context.Context) error { return cause }})
		if result.kind != stopFailed || !errors.Is(result.err, cause) {
			t.Fatalf("result=%+v", result)
		}
	}
}

func TestShutdownStepsPreservesOrderAndErrors(t *testing.T) {
	first, last := errors.New("first failure"), errors.New("last failure")
	var order []int
	var steps []shutdownStep
	for i, err := range []error{first, nil, last} {
		steps = append(steps, shutdownStep{component: componentMedia, phase: phaseClose, run: func(context.Context) error {
			order = append(order, i)
			return err
		}})
	}
	err := runShutdownSteps(t.Context(), steps)
	if !errors.Is(err, first) || !errors.Is(err, last) {
		t.Fatal(err)
	}
	if !slices.Equal(order, []int{0, 1, 2}) {
		t.Fatalf("order=%v", order)
	}
}

func TestShutdownStepsSharesDeadlineAndStopsDependencies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		release := make(chan struct{})
		defer close(release)
		cause := errors.New("earlier failure")
		called := false
		started := time.Now()
		err := runShutdownSteps(ctx, []shutdownStep{
			{component: componentMedia, phase: phaseClose, run: func(context.Context) error {
				time.Sleep(600 * time.Millisecond)
				return cause
			}},
			{component: componentStorage, phase: phaseClose, run: func(context.Context) error { <-release; return nil }},
			{component: componentLogs, phase: phaseClose, run: func(context.Context) error { called = true; return nil }},
		})
		if !errors.Is(err, cause) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if called || time.Since(started) != time.Second {
			t.Fatalf("called=%v elapsed=%s", called, time.Since(started))
		}
	})
}

func TestShutdownReportingDoesNotWaitForBlockedLogConsumer(t *testing.T) {
	release := make(chan struct{})
	connection, err := events.LogSubmitted.Observe(func(context.Context, events.LogRecord) { <-release }, signal.ConnectOptions{Shutdown: signal.Drain})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); _ = connection.Close(context.Background()) }()
	cause := errors.New("cleanup failed")
	done := make(chan error, 1)
	go func() {
		done <- runShutdownSteps(t.Context(), []shutdownStep{
			{component: componentMedia, phase: phaseClose, run: func(context.Context) error { return cause }},
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, cause) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for log consumer")
	}
}

type failingResourceStore struct {
	io.Closer
	cause error
}

func (s failingResourceStore) Close() error {
	return errors.Join(s.Closer.Close(), s.cause)
}

type testConfigLock struct{ closed bool }

func (l *testConfigLock) Close() error { l.closed = true; return nil }

func TestShutdownPreservesIngressAndDependencyPhases(t *testing.T) {
	r, err := prepareResources(t.Context(), appConfigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	cleanupAppResources(t, r)
	var order []string
	lines := testBusinessLines()
	lines.storage = r.store
	for kind, line := range lines.active {
		line.supervisor.(*testLifecycle).begin = func() { order = append(order, kind.String()+" begin") }
		line.supervisor.(*testLifecycle).close = func(ctx context.Context) error {
			order = append(order, kind.String()+" close")
			_, err := r.store.GetByPlatformMessage(ctx, "test", "", "scope", "message")
			return err
		}
	}
	row := &storage.ChatMessage{Platform: "test", ChatID: "scope", PlatformMessageID: "message"}
	if err := r.store.AppendChatMessage(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	line := lines.active[componentPlatform]
	line.waitIngress = func(context.Context) error { order = append(order, "platform ingress"); return nil }
	lines.active[componentPlatform] = line
	lock := &testConfigLock{}
	if err := shutdown(t.Context(), lines, r, lock); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"platform begin", "background begin", "agent begin", "hook begin", "tool begin",
		"platform ingress",
		"background close", "agent close", "hook close", "tool close", "platform close",
	}
	if !slices.Equal(order, want) || !lock.closed {
		t.Fatalf("order=%v lock closed=%v", order, lock.closed)
	}
	if _, err := r.store.GetByPlatformMessage(t.Context(), "test", "", "scope", "message"); err == nil {
		t.Fatal("App did not close history")
	}
}

func TestShutdownContinuesAfterOrdinaryError(t *testing.T) {
	path := appConfigFixture(t)
	r, err := prepareResources(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	cleanupAppResources(t, r)
	cause := errors.New("storage close failure")
	lines := testBusinessLines()
	lines.storage = failingResourceStore{Closer: r.store, cause: cause}
	lock := &testConfigLock{}
	if err := shutdown(t.Context(), lines, r, lock); !errors.Is(err, cause) || !lock.closed {
		t.Fatalf("shutdown error=%v lock closed=%v", err, lock.closed)
	}
	next, err := logging.NewManager("debug", r.config.Storage.SessionsSQLitePath, 1)
	if err != nil {
		t.Fatal("log owner remained occupied", err)
	}
	if err := next.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownTimeoutRetainsHistoryForUnfinishedUser(t *testing.T) {
	for _, phase := range []string{"begin", "close", "done"} {
		t.Run(phase, func(t *testing.T) {
			r, err := prepareResources(t.Context(), appConfigFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			cleanupAppResources(t, r)
			row := &storage.ChatMessage{Platform: "test", ChatID: "scope", PlatformMessageID: "message"}
			if err := r.store.AppendChatMessage(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				defer close(release)
				lines := testBusinessLines()
				lines.storage = r.store
				user := lines.active[componentBackground].supervisor.(*testLifecycle)
				switch phase {
				case "begin":
					user.begin = func() { <-release }
				case "close":
					user.close = func(context.Context) error { <-release; return nil }
				case "done":
					user.close = func(context.Context) error { return errors.New("close failed before exit") }
					user.done = release
				}
				lock := &testConfigLock{}
				started := time.Now()
				err := shutdown(t.Context(), lines, r, lock)
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != shutdownTimeout {
					t.Fatalf("elapsed=%s error=%v", time.Since(started), err)
				}
				if lock.closed {
					t.Fatal("timeout released config lock")
				}
				if _, err := r.store.GetByPlatformMessage(t.Context(), "test", "", "scope", "message"); err != nil {
					t.Fatal("timeout released dependency", err)
				}
			})
		})
	}
}
