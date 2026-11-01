package sqlite

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"elbot/internal/storage"
)

func TestSessionSequenceConcurrentAllocations(t *testing.T) {
	store := newTestStore(t)
	const count = 64
	results := make(chan int64, count)
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			n, err := store.Sessions().NextSequence(t.Context())
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
	store, err := New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.Sessions().NextSequence(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("first=%d err=%v", n, err)
	}
	id := fmt.Sprintf("cl%d", n)
	_, err = store.Sessions().CreateMaterial(t.Context(), storage.SessionMaterialCreate{
		Session: &storage.Session{ID: id}, Messages: []*storage.Message{{ID: "bad", SessionID: "other"}},
	})
	if err == nil {
		t.Fatal("expected material transaction rollback")
	}
	if _, err := store.Sessions().Get(t.Context(), id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("rolled back session exists: %v", err)
	}
	n, err = store.Sessions().NextSequence(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("rollback reused number: %d %v", n, err)
	}
	if err := store.Sessions().Create(t.Context(), &storage.Session{ID: "cl2"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Sessions().Delete(t.Context(), "cl2"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	n, err = store.Sessions().NextSequence(t.Context())
	if err != nil || n != 3 {
		t.Fatalf("reopen reused number: %d %v", n, err)
	}
}

func TestSessionSequenceMigrationPreservesUUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	store, err := New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	legacyID := storage.NewID()
	if err := store.Sessions().Create(t.Context(), &storage.Session{ID: legacyID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE id_sequences; DELETE FROM schema_migrations WHERE version>=28`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Sessions().Get(t.Context(), legacyID); err != nil {
		t.Fatal(err)
	}
	n, err := store.Sessions().NextSequence(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("migrated sequence=%d error=%v", n, err)
	}
	if _, err := store.db.Exec(`UPDATE id_sequences SET value=? WHERE name='session'`, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sessions().NextSequence(t.Context()); err == nil {
		t.Fatal("exhausted sequence must not wrap")
	}
}

func TestSessionCreationRequiresPreallocatedID(t *testing.T) {
	store := newTestStore(t)
	row := &storage.Session{}
	if err := store.Sessions().Create(t.Context(), row); err == nil || row.ID != "" {
		t.Fatalf("storage allocated an ID: %+v %v", row, err)
	}
	if _, err := store.Sessions().CreateMaterial(t.Context(), storage.SessionMaterialCreate{Session: row}); err == nil || row.ID != "" {
		t.Fatalf("material storage allocated an ID: %+v %v", row, err)
	}
}
