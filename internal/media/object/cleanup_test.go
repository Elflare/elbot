package object

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/events"
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
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	var calls []string
	failure := errors.New("remote deletion failed")
	remote.remove = func(context.Context, string) error { calls = append(calls, "remote"); return failure }
	local.remove = func(context.Context, string) error { calls = append(calls, "local"); return nil }
	m.options.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	result, err := m.Cleanup(t.Context(), nil)
	if !errors.Is(err, failure) || result.ResultStatus != events.ResultFailed || !reflect.DeepEqual(calls, []string{"remote"}) {
		t.Fatalf("first cleanup: %#v %v %v", result, err, calls)
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
	result, err = m.Cleanup(t.Context(), nil)
	if err != nil || result.ResultStatus != events.ResultSucceeded || !reflect.DeepEqual(calls, []string{"remote", "remote", "local"}) {
		t.Fatalf("retry: %#v %v %v", result, err, calls)
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
		result events.LogResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := m.Cleanup(ctx, nil); done <- outcome{result, err} }()
	for range cleanupConcurrency {
		awaitTest(t, entered)
	}
	if calls.Load() != cleanupConcurrency {
		t.Fatalf("concurrency = %d", calls.Load())
	}
	// 等待同一轮清理的调用可取消，不阻塞正在执行的工作。
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if _, err := m.Cleanup(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting cancellation = %v", err)
	}
	cancel()
	got := awaitTest(t, done)
	if !errors.Is(got.err, failure) || !errors.Is(got.err, context.Canceled) || got.result.ResultStatus != events.ResultFailed {
		t.Fatalf("cleanup = %#v %v", got.result, got.err)
	}
	if !strings.Contains(got.result.Content, "not_started=4") {
		t.Fatalf("summary = %s", got.result.Content)
	}
	pending, err := store.Media().ClaimOrphans(t.Context(), time.Now().Add(2*OrphanGrace))
	if err != nil || len(pending) != 8 {
		t.Fatalf("pending = %d %v", len(pending), err)
	}
	backend.remove = nil
	result, err := m.Cleanup(t.Context(), nil)
	if err != nil || !strings.Contains(result.Content, "deleted=8") {
		t.Fatalf("retry = %#v %v", result, err)
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
