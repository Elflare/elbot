package control

import (
	"context"
	"elbot/internal/session/share"
	"elbot/internal/storage"
	"testing"
)

type cancelAfterPromotionCommit struct {
	storage.SessionRepository
	cancel context.CancelFunc
}

func (s cancelAfterPromotionCommit) Mutate(ctx context.Context, id string, update func(*storage.Session) error) (*storage.Session, error) {
	row, err := s.SessionRepository.Mutate(ctx, id, update)
	if err == nil {
		s.cancel()
	}
	return row, err
}

func TestPromotionAdoptsAfterCommitDespiteCancellationAndBeginClose(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	scope := privateScope()
	row, err := f.s.PrepareBackground(t.Context(), scope, BackgroundRequest{TaskType: "cron"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.s.deps.Sessions = cancelAfterPromotionCommit{SessionRepository: f.store.Sessions(), cancel: func() { cancel(); f.s.BeginClose() }}
	adopted := false
	f.s.deps.AdoptForeground = func(ctx context.Context, row *storage.Session, b *share.Binding) {
		if ctx.Err() != nil || b != f.s.CurrentBinding(scope) || row.Mode != storage.SessionModeWork {
			t.Fatal("adoption lost committed facts")
		}
		adopted = true
	}
	if _, err := f.s.Resume(ctx, scope, row.ID); err != nil || !adopted {
		t.Fatalf("adoption: %v, called=%v", err, adopted)
	}
	if err := f.s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
