package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type concurrentTestBackend struct {
	Backend
	put     func(context.Context, string, io.Reader, int64, string) (string, error)
	remove  func(context.Context, *storage.Media) error
	presign func(context.Context, *storage.Media, time.Duration) (string, error)
}

func (b *concurrentTestBackend) Put(ctx context.Context, id string, r io.Reader, size int64, mime string) (string, error) {
	if b.put != nil {
		return b.put(ctx, id, r, size, mime)
	}
	return b.Backend.Put(ctx, id, r, size, mime)
}

func (b *concurrentTestBackend) Remove(ctx context.Context, item *storage.Media) error {
	if b.remove != nil {
		return b.remove(ctx, item)
	}
	return b.Backend.Remove(ctx, item)
}

func (b *concurrentTestBackend) PresignGet(ctx context.Context, item *storage.Media, expiry time.Duration) (string, error) {
	if b.presign != nil {
		return b.presign(ctx, item, expiry)
	}
	return "https://media.example/" + item.ObjectKey, nil
}

func newConcurrentTestManager(t *testing.T) *Manager {
	t.Helper()
	store, err := sqlite.New(t.Context(), filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	root := t.TempDir()
	return NewManager(store, root, &LocalBackend{Root: root})
}

func awaitMediaTest[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for media operation")
		var zero T
		return zero
	}
}

func waitObjectUsers(t *testing.T, locks *objectLocks, id string, count int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		locks.mu.Lock()
		entry := locks.entries[id]
		got := 0
		if entry != nil {
			got = entry.users
		}
		locks.mu.Unlock()
		if got == count {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("object lock users = %d, want %d", got, count)
		case <-tick.C:
		}
	}
}

func TestObjectLocksIndependentCancelableAndReclaimed(t *testing.T) {
	var locks objectLocks
	unlock, err := locks.acquire(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		release, err := locks.acquire(ctx, "a")
		if err == nil {
			release()
		}
		done <- err
	}()
	waitObjectUsers(t, &locks, "a", 2)
	other, err := locks.acquire(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	other()
	cancel()
	if err := awaitMediaTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting acquisition = %v", err)
	}
	waitObjectUsers(t, &locks, "a", 1)
	unlock()
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatalf("idle lock entries retained: %d", len(locks.entries))
	}
}

// Seed dual-copy media so the same test exercises remote and local deletion.
func seedCleanupMedia(t *testing.T, m *Manager, count int) map[string][]byte {
	t.Helper()
	data := make(map[string][]byte, count)
	for i := range count {
		body := []byte(fmt.Sprintf("cleanup media %d", i))
		item, err := m.ImportBytes(t.Context(), body, Input{Name: "sample.bin"})
		if err != nil {
			t.Fatal(err)
		}
		item.ObjectKey = "media/" + item.ID
		if err := m.Store.Media().Upsert(t.Context(), item); err != nil {
			t.Fatal(err)
		}
		data[item.ID] = body
	}
	now := time.Now().Add(2 * OrphanGrace)
	m.Now = func() time.Time { return now }
	return data
}

func TestCleanupParallelAllowsOtherMediaAndSameIDReimport(t *testing.T) {
	m := newConcurrentTestManager(t)
	data := seedCleanupMedia(t, m, 8)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	gate := make(chan struct{})
	started := make(chan string, len(data))
	var mu sync.Mutex
	active, peak, calls := 0, 0, 0
	m.Remote = &concurrentTestBackend{
		remove: func(ctx context.Context, item *storage.Media) error {
			mu.Lock()
			active++
			calls++
			peak = max(peak, active)
			mu.Unlock()
			defer func() {
				mu.Lock()
				active--
				mu.Unlock()
			}()
			started <- item.ID
			select {
			case <-gate:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		put: func(_ context.Context, id string, _ io.Reader, _ int64, _ string) (string, error) {
			return "media/" + id, nil
		},
	}
	cleaned := make(chan error, 1)
	go func() { cleaned <- m.Cleanup(ctx) }()
	first := awaitMediaTest(t, started)
	for range 3 {
		awaitMediaTest(t, started)
	}
	// All four deletion requests remain blocked while an unrelated item is used.
	other, err := m.ImportBytes(ctx, []byte("unrelated new media"), Input{Name: "other.bin"})
	if err != nil {
		t.Fatalf("unrelated import blocked or failed: %v", err)
	}
	if body, _, err := m.Read(ctx, other.ID); err != nil || string(body) != "unrelated new media" {
		t.Fatalf("unrelated read = %q, %v", body, err)
	}
	if _, err := m.PresignGet(ctx, other.ID, time.Hour); err != nil {
		t.Fatalf("unrelated upload blocked or failed: %v", err)
	}

	reimported := make(chan error, 1)
	go func() {
		_, err := m.ImportBytes(ctx, data[first], Input{Name: "restored.bin"})
		reimported <- err
	}()
	waitObjectUsers(t, &m.objects, first, 2)
	close(gate)
	if err := awaitMediaTest(t, cleaned); err != nil {
		t.Fatal(err)
	}
	if err := awaitMediaTest(t, reimported); err != nil {
		t.Fatalf("same-ID reimport after deletion: %v", err)
	}
	if body, _, err := m.Read(ctx, first); err != nil || string(body) != string(data[first]) {
		t.Fatalf("reimported object was lost: %q, %v", body, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak != 4 || calls != len(data) || active != 0 {
		t.Fatalf("deletion peak=%d calls=%d active=%d", peak, calls, active)
	}
}

func TestCleanupCancellationRetainsPendingObjectsForRetry(t *testing.T) {
	m := newConcurrentTestManager(t)
	data := seedCleanupMedia(t, m, 7)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan string, len(data))
	m.Remote = &concurrentTestBackend{remove: func(ctx context.Context, item *storage.Media) error {
		started <- item.ID
		<-ctx.Done()
		return ctx.Err()
	}}
	done := make(chan error, 1)
	go func() { done <- m.Cleanup(ctx) }()
	for range 4 {
		awaitMediaTest(t, started)
	}
	cancel()
	if err := awaitMediaTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup cancellation = %v", err)
	}
	if len(started) != 0 {
		t.Fatal("dispatched additional deletes after cancellation")
	}
	for id := range data {
		item, err := m.Store.Media().Get(t.Context(), id)
		if err != nil || !item.Deleting {
			t.Fatalf("pending record %s = %#v, %v", id, item, err)
		}
		if _, err := m.ImportBytes(t.Context(), data[id], Input{}); err == nil {
			t.Fatal("reimport accepted an object awaiting deletion retry")
		}
	}
	m.Remote = &concurrentTestBackend{remove: func(context.Context, *storage.Media) error { return nil }}
	if err := m.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	for id := range data {
		if _, err := m.Store.Media().Get(t.Context(), id); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("record survived retry: %v", err)
		}
	}
}

func TestCleanupConcurrentRoundsAndPartialFailure(t *testing.T) {
	m := newConcurrentTestManager(t)
	data := seedCleanupMedia(t, m, 7)
	var failedID string
	for id := range data {
		failedID = id
		break
	}
	failure := errors.New("remote delete failed")
	var mu sync.Mutex
	calls := map[string]int{}
	m.Remote = &concurrentTestBackend{remove: func(_ context.Context, item *storage.Media) error {
		mu.Lock()
		defer mu.Unlock()
		calls[item.ID]++
		if item.ID == failedID && calls[item.ID] == 1 {
			return failure
		}
		return nil
	}}
	done := make(chan error, 2)
	go func() { done <- m.Cleanup(t.Context()) }()
	go func() { done <- m.Cleanup(t.Context()) }()
	failures := 0
	for range 2 {
		if err := awaitMediaTest(t, done); err != nil {
			if !errors.Is(err, failure) {
				t.Fatal(err)
			}
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("failed cleanup rounds = %d", failures)
	}
	for id := range data {
		if _, err := m.Store.Media().Get(t.Context(), id); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("record survived cleanup rounds: %v", err)
		}
		want := 1
		if id == failedID {
			want = 2
		}
		if calls[id] != want {
			t.Fatalf("delete calls for %s = %d, want %d", id, calls[id], want)
		}
	}
}

func TestConcurrentImportsDeduplicateOnlySameID(t *testing.T) {
	m := newConcurrentTestManager(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	gate := make(chan struct{})
	started := make(chan string, 2)
	local := m.Backend
	var mu sync.Mutex
	puts := 0
	m.Backend = &concurrentTestBackend{Backend: local, put: func(ctx context.Context, id string, r io.Reader, size int64, mime string) (string, error) {
		if mime == "application/x-block-test" {
			mu.Lock()
			puts++
			mu.Unlock()
			started <- id
			select {
			case <-gate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return local.Put(ctx, id, r, size, mime)
	}}
	done := make(chan error, 2)
	importSame := func() {
		_, err := m.ImportBytes(ctx, []byte("same new content"), Input{MIMEType: "application/x-block-test"})
		done <- err
	}
	go importSame()
	id := awaitMediaTest(t, started)
	go importSame()
	waitObjectUsers(t, &m.objects, id, 2)
	if _, err := m.ImportBytes(ctx, []byte("different content"), Input{}); err != nil {
		t.Fatalf("independent import blocked: %v", err)
	}
	close(gate)
	for range 2 {
		if err := awaitMediaTest(t, done); err != nil {
			t.Fatal(err)
		}
	}
	if body, _, err := m.Read(ctx, id); err != nil || string(body) != "same new content" {
		t.Fatalf("imported content = %q, %v", body, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 1 {
		t.Fatalf("same content uploaded %d times", puts)
	}
}

func TestPresignExistingObjectHoldsReferenceUntilSigningFinishes(t *testing.T) {
	m := newConcurrentTestManager(t)
	data := seedCleanupMedia(t, m, 1)
	var id string
	for key := range data {
		id = key
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	m.Remote = &concurrentTestBackend{
		presign: func(ctx context.Context, _ *storage.Media, _ time.Duration) (string, error) {
			started <- struct{}{}
			select {
			case <-gate:
				return "https://media.example/object", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		remove: func(context.Context, *storage.Media) error {
			return errors.New("object being signed must not be deleted")
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err := m.PresignGet(ctx, id, time.Hour)
		done <- err
	}()
	awaitMediaTest(t, started)
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if item, err := m.Store.Media().Get(ctx, id); err != nil || item.Deleting {
		t.Fatalf("object being signed was claimed: %#v, %v", item, err)
	}
	close(gate)
	if err := awaitMediaTest(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestPresignUploadSerializesSameIDAndProtectsFromCleanup(t *testing.T) {
	m := newConcurrentTestManager(t)
	item, err := m.ImportBytes(t.Context(), []byte("upload me"), Input{Name: "sample.bin"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(2 * OrphanGrace)
	m.Now = func() time.Time { return now }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	gate := make(chan struct{})
	started := make(chan struct{}, 2)
	var mu sync.Mutex
	puts, removes := 0, 0
	m.Remote = &concurrentTestBackend{
		put: func(ctx context.Context, id string, _ io.Reader, _ int64, _ string) (string, error) {
			mu.Lock()
			puts++
			mu.Unlock()
			started <- struct{}{}
			select {
			case <-gate:
				return "media/" + id, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		remove: func(context.Context, *storage.Media) error {
			mu.Lock()
			removes++
			mu.Unlock()
			return nil
		},
	}
	done := make(chan error, 3)
	go func() {
		_, err := m.PresignGet(ctx, item.ID, time.Hour)
		done <- err
	}()
	awaitMediaTest(t, started)
	go func() {
		_, err := m.PresignGet(ctx, item.ID, time.Hour)
		done <- err
	}()
	go func() {
		_, err := m.ImportBytes(ctx, []byte("upload me"), Input{})
		done <- err
	}()
	waitObjectUsers(t, &m.objects, item.ID, 3)
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if current, err := m.Store.Media().Get(ctx, item.ID); err != nil || current.Deleting {
		t.Fatalf("upload was claimed for deletion: %#v, %v", current, err)
	}
	close(gate)
	for range 3 {
		if err := awaitMediaTest(t, done); err != nil {
			t.Fatal(err)
		}
	}
	current, err := m.Store.Media().Get(ctx, item.ID)
	if err != nil || current.ObjectKey == "" {
		t.Fatalf("upload metadata lost: %#v, %v", current, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 1 || removes != 0 {
		t.Fatalf("uploads=%d deletes=%d", puts, removes)
	}
}
