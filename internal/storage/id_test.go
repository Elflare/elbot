package storage_test

import (
	"context"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"elbot/internal/request"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func TestBusinessIDsShareReservedRange(t *testing.T) {
	s := storage.NewStoreForTest(t)
	next := storage.IDSequence(s.NextIDSequence)
	message, err := next.MessageID(t.Context())
	if err != nil || message != "m1" {
		t.Fatalf("message=%s err=%v", message, err)
	}
	requests := request.NewManager(next)
	req, _, done, err := requests.Start(t.Context(), request.StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	turns := turn.NewSupervisor(next)
	execution, err := turns.NewExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := turns.AllocateAttempt(t.Context())
	if err != nil || req.ID != "rq2" || execution.ID != "run3" || attempt != "a4" {
		t.Fatalf("request=%s execution=%s attempt=%s err=%v", req.ID, execution.ID, attempt, err)
	}
	var reserved int64
	if err := s.DBForTest().QueryRow("SELECT value FROM id_sequences WHERE name='general'").Scan(&reserved); err != nil || reserved != 1000 {
		t.Fatalf("reserved=%d err=%v", reserved, err)
	}
}

func TestGeneralIDRangesConcurrentStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ids.DBForTest()")
	first, err := storage.OpenMainForTest(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := storage.OpenMainForTest(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// Ensure both connections can wait for the other writer.
	for _, s := range []*storage.Supervisor{first, second} {
		if _, err := s.DBForTest().Exec("PRAGMA busy_timeout=5000"); err != nil {
			t.Fatal(err)
		}
	}
	values := make(chan int64, 2400)
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			s := first
			if i%2 != 0 {
				s = second
			}
			for range 100 {
				n, err := s.NextIDSequence(t.Context())
				if err != nil {
					t.Error(err)
					return
				}
				values <- n
			}
		})
	}
	wg.Wait()
	close(values)
	seen := map[int64]bool{}
	for n := range values {
		if n <= 0 || seen[n] {
			t.Fatalf("duplicate %d", n)
		}
		seen[n] = true
	}
	if len(seen) != 2400 {
		t.Fatal(len(seen))
	}
	var reserved int64
	if err := first.DBForTest().QueryRow("SELECT value FROM id_sequences WHERE name='general'").Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 4000 {
		t.Fatalf("reserved=%d", reserved)
	}
	n, err := first.NextSessionSequence(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("session=%d %v", n, err)
	}
	third, err := storage.OpenMainForTest(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	n, err = third.NextIDSequence(t.Context())
	if err != nil || n != 4001 {
		t.Fatalf("reopen=%d %v", n, err)
	}
}

func TestGeneralIDFailureAndExhaustion(t *testing.T) {
	s := storage.NewStoreForTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.NextIDSequence(ctx); err == nil {
		t.Fatal("canceled allocation succeeded")
	}
	if _, err := s.DBForTest().Exec("UPDATE id_sequences SET value=? WHERE name='general'", int64(math.MaxInt64-2)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{math.MaxInt64 - 1, math.MaxInt64} {
		got, err := s.NextIDSequence(t.Context())
		if err != nil || got != want {
			t.Fatalf("got=%d want=%d err=%v", got, want, err)
		}
	}
	if _, err := s.NextIDSequence(t.Context()); err == nil {
		t.Fatal("exhaustion wrapped")
	}
}
