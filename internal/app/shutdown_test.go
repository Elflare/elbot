package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/logging"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestShutdownStepDeadlineDoesNotWaitForUncooperativeCloser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		release := make(chan struct{})
		defer close(release)
		result := runShutdownStep(ctx, "blocked owner", func(context.Context) error { <-release; return nil })
		if result.kind != stopTimedOut || !errors.Is(result.err, context.DeadlineExceeded) || !strings.Contains(result.err.Error(), "blocked owner") {
			t.Fatalf("result=%+v", result)
		}
		called := false
		result = runShutdownStep(ctx, "next dependency", func(context.Context) error { called = true; return nil })
		if result.kind != stopTimedOut || !errors.Is(result.err, context.DeadlineExceeded) || called {
			t.Fatalf("started a dependency after deadline: result=%+v called=%v", result, called)
		}
	})
}

func TestShutdownStepPreservesNamedCause(t *testing.T) {
	for _, cause := range []error{errors.New("close failed"), context.DeadlineExceeded} {
		result := runShutdownStep(t.Context(), "models", func(context.Context) error { return cause })
		if result.kind != stopFailed || !errors.Is(result.err, cause) || !strings.Contains(result.err.Error(), "models") {
			t.Fatalf("result=%+v", result)
		}
	}
}

func TestShutdownStepsPreservesOrderAndErrors(t *testing.T) {
	first := errors.New("first failure")
	last := errors.New("last failure")
	for _, tt := range []struct {
		name   string
		errors []error
		kind   stopKind
	}{
		{name: "success", errors: []error{nil, nil, nil}, kind: stopCompleted},
		{name: "ordinary failures", errors: []error{first, nil, last}, kind: stopFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var order []int
			var steps []shutdownStep
			for i, err := range tt.errors {
				steps = append(steps, shutdownStep{name: "resource", close: func(context.Context) error {
					order = append(order, i)
					return err
				}})
			}
			result := runShutdownSteps(t.Context(), steps)
			if result.kind != tt.kind || !slices.Equal(order, []int{0, 1, 2}) {
				t.Fatalf("result=%+v order=%v", result, order)
			}
			if tt.kind == stopCompleted && result.err != nil {
				t.Fatal(result.err)
			}
			for _, err := range tt.errors {
				if err != nil && !errors.Is(result.err, err) {
					t.Fatalf("lost error %v: %v", err, result.err)
				}
			}
		})
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
		result := runShutdownSteps(ctx, []shutdownStep{
			{name: "first owner", close: func(context.Context) error {
				time.Sleep(600 * time.Millisecond)
				return cause
			}},
			{name: "blocked owner", close: func(context.Context) error {
				<-release
				return nil
			}},
			{name: "next dependency", close: func(context.Context) error {
				called = true
				return nil
			}},
		})
		if result.kind != stopTimedOut || !errors.Is(result.err, cause) || !errors.Is(result.err, context.DeadlineExceeded) {
			t.Fatalf("result=%+v", result)
		}
		if called || time.Since(started) != time.Second {
			t.Fatalf("called=%v elapsed=%s", called, time.Since(started))
		}
		if !strings.Contains(result.err.Error(), "blocked owner") || strings.Contains(result.err.Error(), "next dependency") {
			t.Fatalf("wrong timeout owner: %v", result.err)
		}
	})
}

type failingResourceStore struct {
	storage.Store
	cause error
}

func (s failingResourceStore) Close() error { return errors.Join(s.Store.Close(), s.cause) }

func TestCloseSharedResourcesContinuesAfterOrdinaryError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	logs, err := logging.NewManager("debug", path, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Close(context.Background()) })
	store, err := sqlite.New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cause := errors.New("injected storage close failure")
	r := &sharedResources{logs: logs, store: failingResourceStore{Store: store, cause: cause}}
	result := closeSharedResources(t.Context(), r)
	if result.kind != stopFailed || !errors.Is(result.err, cause) || !strings.Contains(result.err.Error(), "storage") {
		t.Fatalf("result=%+v", result)
	}
	// 存储关闭失败后仍须释放日志所有权，才能创建下一实例。
	next, err := logging.NewManager("debug", path, 1)
	if err != nil {
		t.Fatal("log owner remained occupied", err)
	}
	t.Cleanup(func() { _ = next.Close(context.Background()) })
}

func TestShutdownOwnsHistoryUntilUsersClose(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "partial construction"}[partial], func(t *testing.T) {
			history, err := sqlite.NewChatHistory(t.Context(), filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = history.Close() })
			row := &storage.ChatMessage{Platform: "test", PlatformScopeID: "scope", PlatformMessageID: "message"}
			if err := history.Repository().Append(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			closed := false
			cause := errors.New("partial owner close failed")
			user := &testLifecycle{close: func(ctx context.Context) error {
				if _, err := history.Repository().GetByPlatformMessage(ctx, "test", "scope", "message"); err != nil {
					t.Error("history closed before user", err)
				}
				closed = true
				if partial {
					return cause
				}
				return nil
			}}
			err = shutdown(t.Context(), businessLines{"tool": {supervisor: user}}, &sharedResources{historyStore: history}, nil)
			if !closed || (partial && !errors.Is(err, cause)) || (!partial && err != nil) {
				t.Fatal(closed, err)
			}
			if _, err := history.Repository().GetByPlatformMessage(t.Context(), "test", "scope", "message"); err == nil {
				t.Fatal("App did not close history")
			}
		})
	}
}

func TestShutdownTimeoutRetainsHistoryForUnfinishedUser(t *testing.T) {
	history, err := sqlite.NewChatHistory(t.Context(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	row := &storage.ChatMessage{Platform: "test", PlatformScopeID: "scope", PlatformMessageID: "message"}
	if err := history.Repository().Append(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		user := &testLifecycle{close: func(context.Context) error { <-release; return nil }}
		err := shutdown(t.Context(), businessLines{"tool": {supervisor: user}}, &sharedResources{historyStore: history}, nil)
		close(release)
		synctest.Wait()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if _, err := history.Repository().GetByPlatformMessage(t.Context(), "test", "scope", "message"); err != nil {
			t.Fatal("timeout released dependency", err)
		}
	})
}
