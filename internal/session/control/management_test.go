package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"elbot/internal/operationresult"
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

func writeTestDraft(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte(id), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDeleteRemovesOnlyCurrentSessionDrafts(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := t.Context(), privateScope()
	previous := f.create(t, scope, "previous")
	keep := writeTestDraft(t, f.s.cfg.DraftRoot, previous.ID)
	current := f.create(t, scope, "current")
	dir := writeTestDraft(t, f.s.cfg.DraftRoot, current.ID)
	if res, err := f.s.Delete(ctx, scope, ""); err != nil || res.Status != operationresult.Success {
		t.Fatalf("delete: %+v %v", res, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted session directory: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(keep, "notes.md")); err != nil || string(data) != previous.ID {
		t.Fatalf("other session draft: %q, %v", data, err)
	}
}

func TestCleanupPreservesBusyPinnedArchivedAndPartialCount(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, id := range []string{"a-delete", "b-busy", "c-failure", "d-pinned", "e-archived"} {
		f.create(t, scope, id)
		writeTestDraft(t, f.s.cfg.DraftRoot, id)
	}
	if result, err := f.s.Pin(ctx, scope, "d-pinned"); err != nil || result.Status != operationresult.Success {
		t.Fatalf("pin: %+v %v", result, err)
	}
	if result, err := f.s.Archive(ctx, scope, "e-archived"); err != nil || result.Status != operationresult.Success {
		t.Fatalf("archive: %+v %v", result, err)
	}
	if result, err := f.s.Resume(ctx, scope, "a-delete"); err != nil || result.Status != operationresult.Success {
		t.Fatalf("resume: %+v %v", result, err)
	}
	secondScope := scope
	secondScope.ChatID, secondScope.IsSuperadmin = "other", true
	if result, err := f.s.Resume(ctx, secondScope, "a-delete"); err != nil || result.Status != operationresult.Success {
		t.Fatalf("resume second scope: %+v %v", result, err)
	}
	first, second := f.s.CurrentBinding(scope), f.s.CurrentBinding(secondScope)
	for _, id := range []string{"a-delete", "b-busy", "c-failure", "d-pinned", "e-archived"} {
		if _, err := f.store.MutateSession(ctx, id, func(row *storage.Session) error { row.UpdatedAt = cutoff.Add(-time.Hour); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	f.setBusy("b-busy")
	failure := errors.New("delete failed")
	f.s.deps.Sessions = cleanupSessions{SessionRepository: f.store, failID: "c-failure", err: failure}
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
	if _, err := os.Stat(filepath.Join(f.s.cfg.DraftRoot, "a-delete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired session draft directory: %v", err)
	}
	for _, id := range []string{"b-busy", "c-failure", "d-pinned", "e-archived"} {
		if _, err := os.Stat(filepath.Join(f.s.cfg.DraftRoot, id, "notes.md")); err != nil {
			t.Errorf("retained session %s lost draft: %v", id, err)
		}
		if _, err := f.store.GetSession(ctx, id); err != nil {
			t.Fatalf("unexpected deletion %s: %v", id, err)
		}
	}
	f.s.deps.Sessions = f.store
	if count, err := f.s.CleanupExpired(ctx, cutoff); err != nil || count != 1 {
		t.Fatalf("continued cleanup: %d %v", count, err)
	}
}
