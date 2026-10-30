package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/events"
	"elbot/internal/media"
)

type expiryMediaAPI struct {
	mediaAPI
	add    func(context.Context, media.Reference) error
	remove func(context.Context, media.Reference) error
}

func pruneMediaForTest(ctx context.Context, b *mediaBridge, now time.Time, closeAll bool) ([]events.LogResult, error) {
	var records []events.LogResult
	err := b.prune(ctx, now, closeAll, func(record events.LogResult) { records = append(records, record) })
	return records, err
}

func (p expiryMediaAPI) AddReference(ctx context.Context, ref media.Reference) error {
	if p.add != nil {
		return p.add(ctx, ref)
	}
	return nil
}
func (p expiryMediaAPI) RemoveReference(ctx context.Context, ref media.Reference) error {
	return p.remove(ctx, ref)
}

func TestSharedExpiryWithoutAccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := simplificationManager(t)
		if err := m.shared.SetWithTTL("short", mustJSON(1), time.Second); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		m.shared.mu.RLock()
		remaining := len(m.shared.items)
		m.shared.mu.RUnlock()
		if remaining != 0 {
			t.Fatal("KV expiry still waits for periodic cleanup or access")
		}
	})
}

func TestMediaExpiryAtDeadlineWithoutScan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		removed := 0
		m, err := NewManager(Options{SharedDir: t.TempDir(), Log: testLog, Notify: testNotify, Media: expiryMediaAPI{remove: func(context.Context, media.Reference) error {
			mu.Lock()
			removed++
			mu.Unlock()
			return nil
		}}})
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close(context.Background())
		synctest.Wait()
		time.Sleep(time.Second) // Expiry deliberately falls between old minute ticks.
		lease, err := m.media.retain(t.Context(), "media")
		if err != nil {
			t.Fatal(err)
		}
		m.media.release(lease)
		time.Sleep(mediaLeaseTTL)
		synctest.Wait()
		mu.Lock()
		count := removed
		mu.Unlock()
		if count != 1 {
			t.Fatalf("deadline cleanup attempts = %d, want 1", count)
		}
	})
}

func TestMediaExpiryRetryBackoffAndLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var attempts []time.Time
		var records []events.LogRecord
		fail := true
		m, err := NewManager(Options{SharedDir: t.TempDir(), Notify: testNotify,
			Log: func(_ context.Context, record events.LogRecord) {
				mu.Lock()
				records = append(records, record)
				mu.Unlock()
			},
			Media: expiryMediaAPI{remove: func(context.Context, media.Reference) error {
				mu.Lock()
				defer mu.Unlock()
				attempts = append(attempts, time.Now())
				if fail {
					return errors.New("release unavailable")
				}
				return nil
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { mu.Lock(); fail = false; mu.Unlock(); _ = m.Close(context.Background()) }()
		lease, err := m.media.retain(t.Context(), "media")
		if err != nil {
			t.Fatal(err)
		}
		m.media.release(lease)
		time.Sleep(mediaLeaseTTL)
		synctest.Wait()
		for i, delay := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
			time.Sleep(delay - time.Second)
			synctest.Wait()
			mu.Lock()
			count := len(attempts)
			mu.Unlock()
			if count != i+1 {
				t.Fatalf("retry %d ran early: attempts=%d", i+1, count)
			}
			time.Sleep(time.Second)
			synctest.Wait()
			mu.Lock()
			count = len(attempts)
			mu.Unlock()
			if count != i+2 {
				t.Fatalf("retry %d missing: attempts=%d", i+1, count)
			}
		}
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		mu.Lock()
		count := len(attempts)
		logged := len(records)
		mu.Unlock()
		if count != 4 || logged != 4 {
			t.Fatalf("retry limit/logs: attempts=%d records=%d", count, logged)
		}
		mu.Lock()
		for i, record := range records {
			if record.Level != slog.LevelError || record.EventName != "prune_hook_media" || !strings.Contains(record.Content, "release unavailable") || !strings.Contains(record.Content, fmt.Sprintf("attempt=%d retry_limit=3", i+1)) {
				t.Errorf("failure log %d: %+v", i, record)
			}
			if i < 3 && !strings.Contains(record.Content, "next_retry_at=") {
				t.Error("retry deadline missing", record)
			}
			if i == 3 && (!strings.Contains(record.Content, "retry_exhausted=true") || strings.Contains(record.Content, "next_retry_at=")) {
				t.Error("exhaustion log incorrect", record)
			}
		}
		mu.Unlock()
		m.media.mu.Lock()
		remaining, queued := len(m.media.leases), m.media.expiry.Len()
		m.media.mu.Unlock()
		if remaining != 1 || queued != 0 {
			t.Fatalf("exhausted lease: remaining=%d queued=%d", remaining, queued)
		}
		// A successful reuse starts a new lease period after exhaustion.
		lease, err = m.media.retain(t.Context(), "media")
		if err != nil {
			t.Fatal(err)
		}
		m.media.release(lease)
		mu.Lock()
		fail = false
		mu.Unlock()
		time.Sleep(mediaLeaseTTL)
		synctest.Wait()
		m.media.mu.Lock()
		remaining = len(m.media.leases)
		m.media.mu.Unlock()
		if remaining != 0 {
			t.Fatal("reused exhausted lease was never cleaned")
		}
	})
}

func TestExpiryQueueUpdatesAndRemovesEntries(t *testing.T) {
	var q expiryQueue
	now := time.Now()
	if !q.set("later", now.Add(10*time.Second)) || !q.set("first", now.Add(time.Second)) {
		t.Fatal("earlier deadline did not change queue head")
	}
	if q.set("last", now.Add(20*time.Second)) {
		t.Fatal("later insertion changed earliest deadline")
	}
	for i := 0; i < 1000; i++ {
		q.set("later", now.Add(time.Duration(i+30)*time.Second))
	}
	if q.Len() != 3 || len(q.byKey) != 3 {
		t.Fatal("renewals accumulated obsolete entries")
	}
	if !q.set("first", now.Add(30*time.Second)) || !q.next().Equal(now.Add(20*time.Second)) {
		t.Fatal("renewal did not move head forward")
	}
	if !q.remove("last") {
		t.Fatal("removing head did not change deadline")
	}
	if q.remove("missing") {
		t.Fatal("missing deletion changed deadline")
	}
	if _, ok := q.popDue(now.Add(29 * time.Second)); ok {
		t.Fatal("popped future entry")
	}
	if key, ok := q.popDue(now.Add(30 * time.Second)); !ok || key != "first" {
		t.Fatal(key, ok)
	}
	q.remove("later")
	if q.Len() != 0 || len(q.byKey) != 0 || !q.next().IsZero() {
		t.Fatal("deleted entries remained indexed")
	}
}

func TestSharedExpiryReschedulesAndPreservesTTLRules(t *testing.T) {
	for _, action := range []string{"get", "overwrite", "permanent", "delete", "failed-cas", "list"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := simplificationManager(t)
				// First let the scheduler wait on a distant deadline, then insert an earlier one.
				if err := m.shared.SetWithTTL("later", mustJSON(1), time.Hour); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if err := m.shared.SetWithTTL("key", mustJSON(1), time.Second); err != nil {
					t.Fatal(err)
				}
				time.Sleep(500 * time.Millisecond)
				switch action {
				case "get":
					if _, ok := m.shared.Get("key"); !ok {
						t.Fatal("missing live key")
					}
				case "overwrite":
					if err := m.shared.SetWithTTL("key", mustJSON(2), time.Second); err != nil {
						t.Fatal(err)
					}
				case "permanent":
					if err := m.shared.SetWithTTL("key", mustJSON(2), 0); err != nil {
						t.Fatal(err)
					}
				case "delete":
					if !m.shared.Delete("key") {
						t.Fatal("delete failed")
					}
				case "failed-cas":
					if ok, err := m.shared.CompareAndSwapWithTTL("key", mustJSON(9), mustJSON(2), time.Hour); err != nil || ok {
						t.Fatal(ok, err)
					}
				case "list":
					if len(m.shared.List("")) != 2 {
						t.Fatal("list failed")
					}
				}
				time.Sleep(600 * time.Millisecond)
				synctest.Wait()
				m.shared.mu.RLock()
				_, present := m.shared.items["key"]
				m.shared.mu.RUnlock()
				want := action == "get" || action == "overwrite" || action == "permanent"
				if present != want {
					t.Fatalf("old deadline: present=%v want=%v", present, want)
				}
				time.Sleep(time.Second)
				synctest.Wait()
				m.shared.mu.RLock()
				_, present = m.shared.items["key"]
				m.shared.mu.RUnlock()
				if present != (action == "permanent") {
					t.Fatal("renewed deadline or ttl=0 incorrect")
				}
				m.shared.Delete("key")
				m.shared.Delete("later")
				synctest.Wait()
				time.Sleep(2 * time.Hour)
				synctest.Wait()
				m.shared.mu.RLock()
				queued := m.shared.expiry.Len()
				m.shared.mu.RUnlock()
				if queued != 0 {
					t.Fatal("deletion left expiry tasks")
				}
			})
		})
	}
}

func TestMediaExpiryWaitsForRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		removed := 0
		m, err := NewManager(Options{SharedDir: t.TempDir(), Log: testLog, Notify: testNotify, Media: expiryMediaAPI{remove: func(context.Context, media.Reference) error { mu.Lock(); removed++; mu.Unlock(); return nil }}})
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close(context.Background())
		lease, err := m.media.retain(t.Context(), "busy")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(mediaLeaseTTL + time.Second)
		synctest.Wait()
		m.media.mu.Lock()
		queued := m.media.expiry.Len()
		m.media.mu.Unlock()
		mu.Lock()
		count := removed
		mu.Unlock()
		if queued != 0 || count != 0 {
			t.Fatal("busy lease remained scheduled or was removed")
		}
		time.Sleep(24 * time.Hour)
		m.media.release(lease)
		synctest.Wait()
		mu.Lock()
		count = removed
		mu.Unlock()
		if count != 1 {
			t.Fatal("release did not trigger expired lease cleanup")
		}
	})
}

func TestMediaExpiryRenewalCancelsPendingRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		attempts := map[string]int{}
		fail := true
		m, err := NewManager(Options{SharedDir: t.TempDir(), Log: testLog, Notify: testNotify, Media: expiryMediaAPI{remove: func(_ context.Context, ref media.Reference) error {
			mu.Lock()
			defer mu.Unlock()
			attempts[ref.MediaID]++
			if fail && ref.MediaID == "retry" {
				return errors.New("retry failure")
			}
			return nil
		}}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { mu.Lock(); fail = false; mu.Unlock(); _ = m.Close(context.Background()) }()
		for _, id := range []string{"retry", "healthy"} {
			lease, err := m.media.retain(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			m.media.release(lease)
		}
		time.Sleep(mediaLeaseTTL)
		synctest.Wait()
		mu.Lock()
		healthy := attempts["healthy"]
		mu.Unlock()
		if healthy != 1 {
			t.Fatal("failed lease delayed healthy cleanup")
		}
		time.Sleep(30 * time.Second)
		lease, err := m.media.retain(t.Context(), "retry")
		if err != nil {
			t.Fatal(err)
		}
		m.media.release(lease)
		time.Sleep(31 * time.Second)
		synctest.Wait()
		mu.Lock()
		count := attempts["retry"]
		mu.Unlock()
		if count != 1 {
			t.Fatal("old retry ran after renewal")
		}
		time.Sleep(mediaLeaseTTL - 31*time.Second)
		synctest.Wait()
		m.media.mu.Lock()
		failures := lease.cleanupFailures
		m.media.mu.Unlock()
		if failures != 1 {
			t.Fatal("successful reuse did not reset failure count")
		}
	})
}

func TestMediaExpiryPartialCleanupRetriesOnlyRemainingWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0600); err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		removed := 0
		m, err := NewManager(Options{SharedDir: t.TempDir(), Log: testLog, Notify: testNotify, Media: expiryMediaAPI{remove: func(context.Context, media.Reference) error { mu.Lock(); removed++; mu.Unlock(); return nil }}})
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close(context.Background())
		lease, err := m.media.retain(t.Context(), "partial")
		if err != nil {
			t.Fatal(err)
		}
		lease.dir = filepath.Join(file, "child") // ENOTDIR makes directory cleanup fail.
		m.media.release(lease)
		time.Sleep(mediaLeaseTTL)
		synctest.Wait()
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		mu.Lock()
		count := removed
		mu.Unlock()
		m.media.mu.Lock()
		remaining := len(m.media.leases)
		m.media.mu.Unlock()
		if count != 1 || remaining != 0 {
			t.Fatalf("partial retry: reference removals=%d remaining=%d", count, remaining)
		}
	})
}

func TestMediaExpiryCloseWaitsForCleanupAndDoesNotBlockKV(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan context.Context, 1)
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		m, err := NewManager(Options{SharedDir: t.TempDir(), Log: testLog, Notify: testNotify, Media: expiryMediaAPI{remove: func(ctx context.Context, _ media.Reference) error {
			entered <- ctx
			<-release // Simulate I/O which does not stop immediately on cancellation.
			return nil
		}}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { unblock(); _ = m.Close(context.Background()) }()
		lease, err := m.media.retain(t.Context(), "blocked")
		if err != nil {
			t.Fatal(err)
		}
		m.media.release(lease)
		time.Sleep(mediaLeaseTTL)
		cleanupCtx := <-entered
		if err := m.shared.SetWithTTL("independent", mustJSON(1), time.Second); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		m.shared.mu.RLock()
		remaining := len(m.shared.items)
		m.shared.mu.RUnlock()
		if remaining != 0 {
			t.Fatal("slow media cleanup blocked KV expiry")
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := m.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if cleanupCtx.Err() != context.Canceled {
			t.Fatal("cleanup did not inherit runtime cancellation")
		}
		select {
		case <-m.Done():
			t.Fatal("Done closed before cleanup returned")
		default:
		}
		unblock()
		synctest.Wait()
		if err := m.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(24 * time.Hour)
		select {
		case <-entered:
			t.Fatal("cleanup restarted after Close")
		default:
		}
	})
}

func TestMediaExpiryFailedReusePreservesRetryDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0600); err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		failAdd := false
		m, err := NewManager(Options{SharedDir: t.TempDir(), Log: testLog, Notify: testNotify, Media: expiryMediaAPI{
			add: func(context.Context, media.Reference) error {
				mu.Lock()
				defer mu.Unlock()
				if failAdd {
					return errors.New("add unavailable")
				}
				return nil
			},
			remove: func(context.Context, media.Reference) error { return nil },
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(file); _ = m.Close(context.Background()) }()
		lease, err := m.media.retain(t.Context(), "partial")
		if err != nil {
			t.Fatal(err)
		}
		lease.dir = filepath.Join(file, "child")
		m.media.release(lease)
		time.Sleep(mediaLeaseTTL)
		synctest.Wait()
		m.media.mu.Lock()
		deadline := m.media.expiry.next()
		m.media.mu.Unlock()
		time.Sleep(30 * time.Second)
		mu.Lock()
		failAdd = true
		mu.Unlock()
		if _, err := m.media.retain(t.Context(), "partial"); err == nil {
			t.Fatal("reuse unexpectedly succeeded")
		}
		m.media.mu.Lock()
		next, failures := m.media.expiry.next(), lease.cleanupFailures
		m.media.mu.Unlock()
		if !next.Equal(deadline) || failures != 1 {
			t.Fatal("failed reuse changed cleanup retry policy")
		}
	})
}
