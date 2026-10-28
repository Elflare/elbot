package object

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/storage"
)

func TestCloseDoesNotWaitForOtherHoldsObjectLock(t *testing.T) {
	m, _, _ := newTestManager(t)
	item := importTestMedia(t, m, "independent close")
	for _, released := range []bool{false, true} {
		held, err := m.Hold(t.Context(), item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if released {
			if err := held.Close(); err != nil {
				t.Fatal(err)
			}
		}
		// 另一个持有上传时会占据同一对象锁。
		unlock, err := m.objects.acquire(t.Context(), item.ID)
		if err != nil {
			t.Fatal(err)
		}
		err = held.Close()
		unlock()
		if err != nil {
			t.Fatalf("released=%v: Close waited for unrelated object work: %v", released, err)
		}
	}
}

func TestConcurrentCloseReleasesOnce(t *testing.T) {
	m, store, _ := newTestManager(t)
	item := importTestMedia(t, m, "concurrent close")
	held, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	m.references = testReferences{ReferenceRepository: m.references, remove: func(ctx context.Context, ref storage.MediaReference) error {
		calls.Add(1)
		return store.MediaReferences().Remove(ctx, ref)
	}}
	var workers sync.WaitGroup
	for range 24 {
		workers.Go(func() {
			if err := held.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("release calls = %d", calls.Load())
	}
}

func TestReaderCloseFailureDoesNotQueueReleasedHold(t *testing.T) {
	m, store, backend := newTestManager(t)
	item := importTestMedia(t, m, "reader close")
	failure := errors.New("reader close failure")
	backend.open = func(context.Context, string) (io.ReadCloser, error) {
		return &testReader{Reader: strings.NewReader("reader close"), closeErr: failure}, nil
	}
	_, _, err := m.Read(t.Context(), item.ID)
	if !errors.Is(err, failure) {
		t.Fatalf("close error lost: %v", err)
	}
	if len(m.pendingReleases) != 0 {
		t.Fatal("successfully released hold queued")
	}
	refs, err := store.MediaReferences().ListMediaIDs(t.Context(), item.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("references = %#v %v", refs, err)
	}
}

func TestExplicitHoldsAreNotReleasedByCleanup(t *testing.T) {
	for _, operation := range []string{"hold", "open"} {
		t.Run(operation, func(t *testing.T) {
			m, store, _ := newTestManager(t)
			item := importTestMedia(t, m, "caller owns release")
			var close func() error
			if operation == "hold" {
				held, err := m.Hold(t.Context(), item.ID)
				if err != nil {
					t.Fatal(err)
				}
				close = held.Close
			} else {
				reader, _, err := m.Open(t.Context(), item.ID)
				if err != nil {
					t.Fatal(err)
				}
				close = reader.Close
			}
			failure := errors.New("release failed")
			m.references = testReferences{ReferenceRepository: m.references, remove: func(context.Context, storage.MediaReference) error { return failure }}
			if err := close(); !errors.Is(err, failure) {
				t.Fatalf("Close = %v", err)
			}
			m.references = store.MediaReferences()
			m.options.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
			if _, err := m.Cleanup(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			refs, err := store.MediaReferences().ListMediaIDs(t.Context(), item.ID)
			if err != nil || len(refs) != 1 {
				t.Fatalf("caller hold was taken over: %#v %v", refs, err)
			}
			if err := close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOwnedReleaseRetryFailureStopsCleanup(t *testing.T) {
	m, store, _ := newTestManager(t)
	item := importTestMedia(t, m, "pending release")
	orphan := importTestMedia(t, m, "other orphan")
	failure := errors.New("release unavailable")
	m.references = testReferences{ReferenceRepository: m.references, remove: func(context.Context, storage.MediaReference) error { return failure }}
	if _, _, err := m.Read(t.Context(), item.ID); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	m.options.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	for range 2 {
		result, err := m.Cleanup(t.Context(), nil)
		if !errors.Is(err, failure) || result.ResultStatus != events.ResultFailed || !strings.Contains(result.Content, "stage=release_owned") {
			t.Fatalf("cleanup = %#v %v", result, err)
		}
		record, err := store.Media().Get(t.Context(), orphan.ID)
		if err != nil || record.Deleting {
			t.Fatalf("cleanup continued after release failure: %#v %v", record, err)
		}
		if len(m.pendingReleases) != 1 {
			t.Fatalf("retry lost/duplicated item: %d", len(m.pendingReleases))
		}
	}
	m.references = store.MediaReferences()
	// 释放完成后的正常时间内仍保留宽限期。
	m.options.Now = storage.Now
	if _, err := m.Cleanup(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Info(t.Context(), item.ID); err != nil {
		t.Fatalf("released object ignored grace period: %v", err)
	}
	m.options.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	if _, err := m.Cleanup(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Info(t.Context(), item.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("released orphan survived: %v", err)
	}
}

func TestOwnedReleaseCancellationPreservesPendingAndNewItems(t *testing.T) {
	for _, failCurrent := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "failure and canceled"}[failCurrent], func(t *testing.T) {
			m, store, _ := newTestManager(t)
			first := importTestMedia(t, m, "first pending")
			second := importTestMedia(t, m, "second pending")
			third := importTestMedia(t, m, "new pending")
			failure := errors.New("release failed")
			m.references = testReferences{ReferenceRepository: m.references, remove: func(context.Context, storage.MediaReference) error { return failure }}
			for _, id := range []string{first.ID, second.ID} {
				if _, _, err := m.Read(t.Context(), id); !errors.Is(err, failure) {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var secondCalls atomic.Int32
			m.references = testReferences{ReferenceRepository: store.MediaReferences(), remove: func(ctx context.Context, ref storage.MediaReference) error {
				switch ref.MediaID {
				case first.ID:
					close(entered)
					<-release
					// Cleanup 的取消不提前终止已开始的独立释放。
					if ctx.Err() != nil {
						t.Error("release inherited Cleanup cancellation")
					}
					if failCurrent {
						return failure
					}
					return store.MediaReferences().Remove(ctx, ref)
				case second.ID:
					secondCalls.Add(1)
				}
				return failure
			}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type outcome struct {
				result events.LogResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() { result, err := m.Cleanup(ctx, nil); done <- outcome{result, err} }()
			awaitTest(t, entered)
			// 当前批次正在重试时，另一个便捷调用登记新的失败释放。
			if _, _, err := m.Read(t.Context(), third.ID); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			cancel()
			select {
			case <-done:
				t.Fatal("Cleanup returned before in-flight release ended")
			default:
			}
			close(release)
			got := awaitTest(t, done)
			wantStatus, wantPending := events.ResultCanceled, 2
			if failCurrent {
				wantStatus, wantPending = events.ResultFailed, 3
			}
			if !errors.Is(got.err, context.Canceled) || errors.Is(got.err, failure) != failCurrent || got.result.ResultStatus != wantStatus {
				t.Fatalf("cleanup = %#v %v", got.result, got.err)
			}
			if secondCalls.Load() != 0 || len(m.pendingReleases) != wantPending {
				t.Fatalf("remaining=%d unstarted calls=%d", len(m.pendingReleases), secondCalls.Load())
			}
			m.references = store.MediaReferences()
			if _, err := m.Cleanup(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{first.ID, second.ID, third.ID} {
				refs, err := store.MediaReferences().ListMediaIDs(t.Context(), id)
				if err != nil || len(refs) != 0 {
					t.Fatalf("lost pending release: %#v %v", refs, err)
				}
			}
			if len(m.pendingReleases) != 0 {
				t.Fatal("successful releases retained")
			}
		})
	}
}

func TestOwnedReleaseFailureRecoveredByCleanup(t *testing.T) {
	for _, operation := range []string{"read", "export", "validate", "open failure"} {
		t.Run(operation, func(t *testing.T) {
			m, store, backend := newTestManager(t)
			item := importTestMedia(t, m, "owned release")
			failure := errors.New("temporary release failure")
			m.references = testReferences{ReferenceRepository: m.references, remove: func(context.Context, storage.MediaReference) error { return failure }}
			var err error
			switch operation {
			case "read":
				_, _, err = m.Read(t.Context(), item.ID)
			case "export":
				_, err = m.Export(t.Context(), item.ID, filepath.Join(t.TempDir(), "export"))
			case "validate":
				_, err = m.ValidateImage(t.Context(), item.ID)
			case "open failure":
				delete(backend.data, item.ID)
				_, _, err = m.Read(t.Context(), item.ID)
			}
			if !errors.Is(err, failure) {
				t.Fatalf("release failure lost: %v", err)
			}
			m.references = store.MediaReferences()
			if _, err := m.Cleanup(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			refs, err := store.MediaReferences().ListMediaIDs(t.Context(), item.ID)
			if err != nil || len(refs) != 0 {
				t.Fatalf("owned release lost: references=%d error=%v", len(refs), err)
			}
		})
	}
}
