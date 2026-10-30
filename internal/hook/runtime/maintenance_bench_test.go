package runtime

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"elbot/internal/media"
)

// These benchmarks compare existing maintenance with scheduling-only models.
// The models do not replace production KV/media behavior or perform media I/O.
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

type benchmarkDeadline struct {
	at    time.Time
	index int
}
type benchmarkDeadlines []*benchmarkDeadline

func (h benchmarkDeadlines) Len() int           { return len(h) }
func (h benchmarkDeadlines) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h benchmarkDeadlines) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index, h[j].index = i, j }
func (h *benchmarkDeadlines) Push(value any) {
	item := value.(*benchmarkDeadline)
	item.index = len(*h)
	*h = append(*h, item)
}
func (h *benchmarkDeadlines) Pop() any {
	old := *h
	item := old[len(old)-1]
	*h = old[:len(old)-1]
	return item
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
			h := make(benchmarkDeadlines, count)
			items := make([]*benchmarkDeadline, count)
			for i := range h {
				h[i] = &benchmarkDeadline{at: time.Now().Add(time.Hour), index: i}
				items[i] = h[i]
			}
			heap.Init(&h)
			timer := time.NewTimer(time.Hour)
			defer timer.Stop()
			var mu sync.Mutex
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				mu.Lock()
				item := items[i%count]
				old := h[0].at
				item.at = time.Now().Add(time.Hour)
				heap.Fix(&h, item.index)
				if !old.Equal(h[0].at) {
					timer.Reset(time.Until(h[0].at))
				}
				mu.Unlock()
			}
		})
	}
}

func BenchmarkMaintenanceSetup(b *testing.B) {
	const count = 10000
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
			h := make(benchmarkDeadlines, count)
			for j := range h {
				h[j] = &benchmarkDeadline{at: time.Now().Add(time.Hour), index: j}
			}
			heap.Init(&h)
			timer := time.NewTimer(time.Until(h[0].at))
			timer.Stop()
		}
	})
}

func BenchmarkMediaMaintenance(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		for _, mode := range []string{"live", "busy-expired", "failed-retry"} {
			b.Run(fmt.Sprintf("%s/%d", mode, count), func(b *testing.B) {
				bridge := newMediaBridge(nil)
				now := time.Now()
				for i := 0; i < count; i++ {
					lease := &mediaLease{expires: now.Add(time.Hour)}
					if mode == "busy-expired" {
						lease.expires = now.Add(-time.Second)
						lease.busy = make(chan struct{})
					}
					bridge.leases[fmt.Sprintf("media-%d", i)] = lease
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
					_, _ = bridge.prune(b.Context(), now, false)
				}
			})
		}
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
