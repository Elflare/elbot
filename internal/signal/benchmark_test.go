package signal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func BenchmarkSignalSync(b *testing.B) {
	for _, subscribers := range []int{0, 1, 8, 64} {
		b.Run(fmt.Sprint(subscribers), func(b *testing.B) {
			s := New[int, Empty]("benchmark")
			defer s.Close(context.Background())
			for range subscribers {
				if _, err := s.Observe(func(context.Context, int) error { return nil }, ConnectOptions{}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.Emit(context.Background(), 1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSignalConcurrent(b *testing.B) {
	for _, subscribers := range []int{1, 8} {
		b.Run(fmt.Sprint(subscribers), func(b *testing.B) {
			s := New[int, int]("concurrent", Options[int]{Capacity: 4096})
			defer s.Close(context.Background())
			for range subscribers {
				_, _ = s.Connect(func(_ context.Context, n int) (int, error) { return n, nil }, ConnectOptions{})
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := s.Emit(context.Background(), 1); err != nil {
						b.Error(err)
					}
				}
			})
		})
	}
}

func BenchmarkSignalAsyncBatch(b *testing.B) {
	for _, subscribers := range []int{1, 8} {
		b.Run(fmt.Sprint(subscribers), func(b *testing.B) {
			s := New[int, int]("batch", Options[int]{Capacity: 4096})
			defer s.Close(context.Background())
			for range subscribers {
				_, _ = s.Connect(func(_ context.Context, n int) (int, error) { return n, nil }, ConnectOptions{})
			}
			var batch [64]*Emission[int]
			b.ReportAllocs()
			for b.Loop() {
				for i := range batch {
					var err error
					batch[i], err = s.EmitAsync(context.Background(), i)
					if err != nil {
						b.Fatal(err)
					}
				}
				for _, e := range batch {
					if _, err := e.Wait(context.Background()); err != nil {
						b.Fatal(err)
					}
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/64, "ns/emission")
		})
	}
}

func BenchmarkSignalQueueFull(b *testing.B) {
	s := New[int, int]("full", Options[int]{Concurrency: 1, Capacity: 1})
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	_, _ = s.Connect(func(context.Context, int) (int, error) { once.Do(func() { close(started) }); <-release; return 0, nil }, ConnectOptions{})
	defer func() { close(release); _ = s.Close(context.Background()) }()
	_, _ = s.EmitAsync(context.Background(), 0)
	<-started
	_, _ = s.EmitAsync(context.Background(), 0)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.EmitAsync(context.Background(), 0); !errors.Is(err, ErrQueueFull) {
			b.Fatal(err)
		}
	}
}

func BenchmarkSignalKeyed(b *testing.B) {
	keys := [...]string{"a", "b", "c", "d", "e", "f", "g", "h"}
	s := New[int, int]("keyed", Options[int]{Capacity: 4096, Key: func(n int) string { return keys[n%len(keys)] }})
	defer s.Close(context.Background())
	_, _ = s.Connect(func(_ context.Context, n int) (int, error) { return n, nil }, ConnectOptions{})
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		n := 0
		for pb.Next() {
			if _, err := s.Emit(context.Background(), n); err != nil {
				b.Error(err)
			}
			n++
		}
	})
}

func BenchmarkSignalForward(b *testing.B) {
	up := New[int, int]("router", Options[int]{Concurrency: 1, Single: true})
	down := New[int, int]("platform", Options[int]{Single: true})
	defer up.Close(context.Background())
	defer down.Close(context.Background())
	_, _ = down.Connect(func(_ context.Context, n int) (int, error) { return n, nil }, ConnectOptions{})
	_, _ = up.ConnectForward(func(ctx context.Context, n int) *Emission[int] {
		e, err := down.EmitAsync(ctx, n)
		if e == nil {
			return Resolved(Result[int]{Err: err})
		}
		return e
	}, ConnectOptions{})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := up.Emit(context.Background(), 1); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQueueRoundTrip(b *testing.B) {
	q, err := NewQueue(QueueOptions{Name: "benchmark", WaitForCapacity: true})
	if err != nil {
		b.Fatal(err)
	}
	defer q.Close(context.Background())
	done := make(chan struct{}, 1)
	task := Task{Run: func(context.Context) error { done <- struct{}{}; return nil }}
	b.ReportAllocs()
	for b.Loop() {
		if err := q.Submit(context.Background(), task); err != nil {
			b.Fatal(err)
		}
		<-done
	}
}

func BenchmarkQueueThroughput(b *testing.B) {
	q, err := NewQueue(QueueOptions{Name: "benchmark", WaitForCapacity: true})
	if err != nil {
		b.Fatal(err)
	}
	defer q.Close(context.Background())
	task := Task{Shutdown: Drain, Run: func(context.Context) error { return nil }}
	b.ReportAllocs()
	for b.Loop() {
		if err := q.Submit(context.Background(), task); err != nil {
			b.Fatal(err)
		}
	}
}
