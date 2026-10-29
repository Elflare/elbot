package control

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"elbot/internal/storage"
)

type cleanupSessions struct {
	storage.SessionRepository
	failID string
	err    error
}

func (r cleanupSessions) ListExpiredIDs(ctx context.Context, cutoff time.Time) ([]string, error) {
	ids, err := r.SessionRepository.ListExpiredIDs(ctx, cutoff)
	slices.Sort(ids)
	return ids, err
}

func (r cleanupSessions) DeleteIfExpired(ctx context.Context, id string, cutoff time.Time) (bool, error) {
	if id == r.failID {
		return false, r.err
	}
	return r.SessionRepository.DeleteIfExpired(ctx, id, cutoff)
}

func TestCleanupPreservesBusyPinnedArchivedAndPartialCount(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, id := range []string{"a-delete", "b-busy", "c-failure", "d-pinned", "e-archived"} {
		f.create(t, scope, id)
	}
	if _, err := f.s.Pin(ctx, scope, "d-pinned"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Archive(ctx, scope, "e-archived"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Resume(ctx, scope, "a-delete"); err != nil {
		t.Fatal(err)
	}
	secondScope := scope
	secondScope.PlatformScopeID, secondScope.IsCLI = "other", true
	if _, err := f.s.Resume(ctx, secondScope, "a-delete"); err != nil {
		t.Fatal(err)
	}
	first, second := f.s.CurrentBinding(scope), f.s.CurrentBinding(secondScope)
	for _, id := range []string{"a-delete", "b-busy", "c-failure", "d-pinned", "e-archived"} {
		if _, err := f.store.Sessions().Mutate(ctx, id, func(row *storage.Session) error { row.UpdatedAt = cutoff.Add(-time.Hour); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	f.setBusy("b-busy")
	failure := errors.New("delete failed")
	f.s.deps.Sessions = cleanupSessions{SessionRepository: f.store.Sessions(), failID: "c-failure", err: failure}
	beforeSessions, beforeBindings := f.releaseCounts()
	count, err := f.s.CleanupExpired(ctx, cutoff)
	if count != 1 || !errors.Is(err, failure) {
		t.Fatalf("partial cleanup: %d %v", count, err)
	}
	if first.Valid() || second.Valid() || f.s.CurrentBinding(scope) != nil || f.s.CurrentBinding(secondScope) != nil {
		t.Fatal("deleted session retained a binding")
	}
	if sessions, bindings := f.releaseCounts(); sessions != beforeSessions+1 || bindings != beforeBindings+2 {
		t.Fatalf("cleanup releases: %d %d", sessions-beforeSessions, bindings-beforeBindings)
	}
	for _, id := range []string{"b-busy", "c-failure", "d-pinned", "e-archived"} {
		if _, err := f.store.Sessions().Get(ctx, id); err != nil {
			t.Fatalf("unexpected deletion %s: %v", id, err)
		}
	}
	f.s.deps.Sessions = f.store.Sessions()
	if count, err := f.s.CleanupExpired(ctx, cutoff); err != nil || count != 1 {
		t.Fatalf("continued cleanup: %d %v", count, err)
	}
}
