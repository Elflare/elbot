package maindb

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"elbot/internal/storage/share"
)

func TestSessionSequenceConcurrentAllocations(t *testing.T) {
	store := NewStoreForTest(t)
	const count = 64
	results := make(chan int64, count)
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			n, err := store.NextSessionSequence(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			results <- n
		})
	}
	workers.Wait()
	close(results)
	seen := map[int64]bool{}
	for n := range results {
		if n < 1 || n > count || seen[n] {
			t.Fatalf("invalid or repeated sequence %d", n)
		}
		seen[n] = true
	}
	if len(seen) != count {
		t.Fatalf("allocated %d of %d", len(seen), count)
	}
}

func TestSessionSequenceSurvivesRollbackDeletionAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sequence.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.NextSessionSequence(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("first=%d err=%v", n, err)
	}
	id := fmt.Sprintf("cl%d", n)
	err = store.CreateMaterial(t.Context(), SessionMaterialCreate{
		Session: &Session{ID: id}, Messages: []*Message{{ID: "bad", SessionID: "other"}},
	})
	if err == nil {
		t.Fatal("expected material transaction rollback")
	}
	if _, err := store.GetSession(t.Context(), id); !errors.Is(err, share.ErrNotFound) {
		t.Fatalf("rolled back session exists: %v", err)
	}
	n, err = store.NextSessionSequence(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("rollback reused number: %d %v", n, err)
	}
	if err := store.CreateSession(t.Context(), &Session{ID: "cl2"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSession(t.Context(), "cl2"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	n, err = store.NextSessionSequence(t.Context())
	if err != nil || n != 3 {
		t.Fatalf("reopen reused number: %d %v", n, err)
	}
}

func TestSessionSequenceMigrationPreservesUUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	legacyID := share.NewID()
	if err := store.CreateSession(t.Context(), &Session{ID: legacyID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE id_sequences; DELETE FROM schema_migrations WHERE version IN (28,29)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.GetSession(t.Context(), legacyID); err != nil {
		t.Fatal(err)
	}
	n, err := store.NextSessionSequence(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("migrated sequence=%d error=%v", n, err)
	}
	if _, err := store.db.Exec(`UPDATE id_sequences SET value=? WHERE name='session'`, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NextSessionSequence(t.Context()); err == nil {
		t.Fatal("exhausted sequence must not wrap")
	}
}
