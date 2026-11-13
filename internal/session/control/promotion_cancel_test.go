package control

import (
	"context"
	"elbot/internal/operationresult"
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
	prepared, err := f.s.PrepareBackground(t.Context(), scope, BackgroundRequest{TaskType: "cron"})
	if err != nil || prepared.Status != operationresult.Success {
		t.Fatalf("prepare: %+v %v", prepared, err)
	}
	row := prepared.Data
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
	if result, err := f.s.Resume(ctx, scope, row.ID); err != nil || result.Status != operationresult.Success || !adopted {
		t.Fatalf("adoption: %+v, %v, called=%v", result, err, adopted)
	}
	if err := f.s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
