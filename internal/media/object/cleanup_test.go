package object

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/signal"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/storage"
)

func awaitTest[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
		var zero T
		return zero
	}
}

func TestCleanupDeletesRemoteThenLocalAndRetries(t *testing.T) {
	m, store, local := newTestManager(t)
	item := importTestMedia(t, m, "cleanup")
	remote := &testBackend{}
	m.remote = remote
	held, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.SignedURL(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := held.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	var calls []string
	failure := errors.New("remote deletion failed")
	remote.remove = func(context.Context, string) error { calls = append(calls, "remote"); return failure }
	local.remove = func(context.Context, string) error { calls = append(calls, "local"); return nil }
	m.options.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	logs := make(chan events.LogRecord, 8)
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) {
		if r.Module == "media" && strings.Contains(r.Content, item.ID) {
			logs <- r
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	err = m.Cleanup(t.Context(), nil)
	if err != nil || !reflect.DeepEqual(calls, []string{"remote"}) {
		t.Fatal("first cleanup:   ", err, calls)
	}
	select {
	case r := <-logs:
		if r.Level != slog.LevelWarn || r.ResultStatus != events.ResultFailed || !strings.Contains(r.Content, failure.Error()) {
			t.Fatalf("remote deletion log=%+v", r)
		}
	default:
		t.Fatal("remote deletion failure missing")
	}
	record, err := store.Media().Get(t.Context(), item.ID)
	if err != nil || !record.Deleting {
		t.Fatalf("retry state = %#v %v", record, err)
	}
	if _, err := m.Hold(t.Context(), item.ID); err == nil {
		t.Fatal("acquired deleting object")
	}
	if err := m.AddReference(t.Context(), Reference{MediaID: item.ID, OwnerType: "test", OwnerID: "late", Purpose: "content"}); err == nil {
		t.Fatal("reference added after claim")
	}
	remote.remove = func(context.Context, string) error { calls = append(calls, "remote"); return nil }
	err = m.Cleanup(t.Context(), nil)
	if err != nil || !reflect.DeepEqual(calls, []string{"remote", "remote", "local"}) {
		t.Fatal("retry:   ", err, calls)
	}
	if _, err := store.Media().Get(t.Context(), item.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("record survived: %v", err)
	}
	if _, err := store.Media().GetSignedURL(t.Context(), item.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("signature survived: %v", err)
	}
}

func TestCleanupConcurrencyCancellationAndFailure(t *testing.T) {
	m, store, backend := newTestManager(t)
	for i := range 8 {
		importTestMedia(t, m, fmt.Sprint("cleanup-", i))
	}
	m.options.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	entered := make(chan struct{}, 8)
	failure := errors.New("independent failure")
	var calls atomic.Int32
	logs := captureMediaLogs(t)
	backend.remove = func(ctx context.Context, _ string) error {
		n := calls.Add(1)
		entered <- struct{}{}
		<-ctx.Done()
		if n == 1 {
			return failure
		}
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() { err := m.Cleanup(ctx, nil); done <- outcome{err} }()
	for range cleanupConcurrency {
		awaitTest(t, entered)
	}
	if calls.Load() != cleanupConcurrency {
		t.Fatalf("concurrency = %d", calls.Load())
	}
	// 等待同一轮清理的调用可取消，不阻塞正在执行的工作。
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if err := m.Cleanup(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting cancellation = %v", err)
	}
	cancel()
	got := awaitTest(t, done)
	requireMediaLog(t, logs, failure.Error())
	if errors.Is(got.err, failure) || !errors.Is(got.err, context.Canceled) {
		t.Fatal(got.err)
	}

	pending, err := store.Media().ClaimOrphans(t.Context(), time.Now().Add(2*OrphanGrace))
	if err != nil || len(pending) != 8 {
		t.Fatalf("pending = %d %v", len(pending), err)
	}
	backend.remove = nil
	err = m.Cleanup(t.Context(), nil)
	if err != nil {
		t.Fatal("retry =  ", err)
	}
}

func TestObjectLocksCancellationAndReuse(t *testing.T) {
	var locks objectLocks
	unlock, err := locks.acquire(t.Context(), "first")
	if err != nil {
		t.Fatal(err)
	}
	other, err := locks.acquire(t.Context(), "second")
	if err != nil {
		t.Fatal(err)
	}
	other()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		release, err := locks.acquire(ctx, "first")
		if release != nil {
			release()
		}
		done <- err
	}()
	cancel()
	if err := awaitTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait = %v", err)
	}
	unlock()
	if len(locks.entries) != 0 {
		t.Fatal("canceled waiter leaked lock")
	}
	unlock, err = locks.acquire(t.Context(), "first")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestCleanupReturnsCancellationAndLogsIndependentFailure(t *testing.T) {
	m, _, backend := newTestManager(t)
	importTestMedia(t, m, "cancel-detail")
	m.options.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	failure := errors.New("local filesystem failed")
	backend.remove = func(context.Context, string) error { cancel(); return failure }
	logs := captureMediaLogs(t)
	if err := m.Cleanup(ctx, nil); !errors.Is(err, context.Canceled) || errors.Is(err, failure) {
		t.Fatalf("cleanup error=%v", err)
	}
	requireMediaLog(t, logs, failure.Error())
}
