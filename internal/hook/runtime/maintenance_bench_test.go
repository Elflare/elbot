package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/media"
)

// Benchmarks use the production expiry index. Timer comparisons measure only
// scheduling costs; media failures use a probe without filesystem/network I/O.
func BenchmarkSharedMaintenance(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		for _, operation := range []string{"get", "set", "cas", "prune", "evict"} {
			b.Run(fmt.Sprintf("%s/%d", operation, count), func(b *testing.B) {
				s := NewSharedState()
				s.maxEntries = count
				keys := make([]string, count+1)
				now := time.Now()
				for i := range keys {
					keys[i] = fmt.Sprintf("key-%05d", i)
					if i < count {
						entry := sharedEntry{value: json.RawMessage(`1`), ttl: time.Hour, lastAccess: now}
						s.items[keys[i]] = entry
						s.size += sharedEntrySize(keys[i], entry)
						s.scheduleExpiryLocked(keys[i], entry)
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					key := keys[i%count]
					switch operation {
					case "get":
						s.Get(key)
					case "set":
						if err := s.SetWithTTL(key, json.RawMessage(`1`), time.Hour); err != nil {
							b.Fatal(err)
						}
					case "cas":
						if ok, err := s.CompareAndSwapWithTTL(key, json.RawMessage(`1`), json.RawMessage(`1`), time.Hour); err != nil || !ok {
							b.Fatal(ok, err)
						}
					case "prune":
						s.PruneExpired()
					case "evict":
						if err := s.SetWithTTL(keys[(i+count)%(count+1)], json.RawMessage(`1`), time.Hour); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func BenchmarkMaintenanceRenewal(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("per-item/%d", count), func(b *testing.B) {
			timers := make([]*time.Timer, count)
			for i := range timers {
				timers[i] = time.AfterFunc(time.Hour, func() {})
			}
			defer func() {
				for _, timer := range timers {
					timer.Stop()
				}
			}()
			var mu sync.Mutex
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				mu.Lock()
				timers[i%count].Reset(time.Hour)
				mu.Unlock()
			}
		})
		b.Run(fmt.Sprintf("nearest/%d", count), func(b *testing.B) {
			var q expiryQueue
			keys := make([]string, count)
			for i := range keys {
				keys[i] = fmt.Sprintf("key-%d", i)
				q.set(keys[i], time.Now().Add(time.Hour))
			}
			timer := time.NewTimer(time.Hour)
			defer timer.Stop()
			var mu sync.Mutex
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				mu.Lock()
				if q.set(keys[i%count], time.Now().Add(time.Hour)) {
					timer.Reset(time.Until(q.next()))
				}
				mu.Unlock()
			}
		})
	}
}

func BenchmarkMaintenanceSetup(b *testing.B) {
	const count = 10000
	keys := make([]string, count)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
	}
	b.Run("per-item", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			timers := make([]*time.Timer, count)
			for j := range timers {
				timers[j] = time.AfterFunc(time.Hour, func() {})
			}
			for _, timer := range timers {
				timer.Stop()
			}
		}
	})
	b.Run("nearest", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var q expiryQueue
			for _, key := range keys {
				q.set(key, time.Now().Add(time.Hour))
			}
			timer := time.NewTimer(time.Until(q.next()))
			timer.Stop()
		}
	})
}

func BenchmarkMediaMaintenance(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		for _, mode := range []string{"live", "busy-waiting-release", "failed-retry"} {
			b.Run(fmt.Sprintf("%s/%d", mode, count), func(b *testing.B) {
				bridge := newMediaBridge(nil)
				now := time.Now()
				for i := 0; i < count; i++ {
					lease := &mediaLease{expires: now.Add(time.Hour)}
					if mode == "busy-waiting-release" {
						lease.expires = now.Add(-time.Second)
						lease.busy = make(chan struct{})
					}
					id := fmt.Sprintf("media-%d", i)
					bridge.leases[id] = lease
					bridge.scheduleExpiryLocked(id, lease.expires)
				}
				if mode == "busy-waiting-release" {
					_ = bridge.prune(b.Context(), now, false, func(events.LogResult) {})
				}
				// A single failed lease among live leases: simulate reference-release
				// failure without filesystem/network cost, preserving it for retry.
				if mode == "failed-retry" {
					errBenchmarkRelease := errors.New("release failed")
					bridge.api = mediaProbe{remove: func(_ context.Context, _ media.Reference) error { return errBenchmarkRelease }}
					bridge.leases["media-0"].expires = now.Add(-time.Second)
					bridge.leases["media-0"].retained = true
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if mode == "failed-retry" {
						// Each iteration models one due failed attempt, not the idle
						// time between retries or an already exhausted lease.
						now = time.Now()
						bridge.leases["media-0"].cleanupFailures = 0
						bridge.scheduleExpiryLocked("media-0", now)
					}
					_ = bridge.prune(b.Context(), now, false, func(events.LogResult) {})
				}
			})
		}
	}
}

func BenchmarkExpiryQueueDrain(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("%d", count), func(b *testing.B) {
			keys := make([]string, count)
			for i := range keys {
				keys[i] = fmt.Sprintf("key-%d", i)
			}
			now := time.Now()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				var q expiryQueue
				for _, key := range keys {
					q.set(key, now)
				}
				b.StartTimer()
				for j := 0; j < count; j++ {
					q.popDue(now)
				}
			}
		})
	}
}

func BenchmarkMaintenanceExpiryBurst(b *testing.B) {
	const count = 1000
	b.Run("per-item", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var wg sync.WaitGroup
			wg.Add(count)
			for j := 0; j < count; j++ {
				time.AfterFunc(0, wg.Done)
			}
			wg.Wait()
		}
	})
	b.Run("one-notification", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var wg sync.WaitGroup
			wg.Add(count)
			time.AfterFunc(0, func() {
				for j := 0; j < count; j++ {
					wg.Done()
				}
			})
			wg.Wait()
		}
	})
}
