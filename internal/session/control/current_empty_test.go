package control

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/storage"
)

func TestNoCurrentSessionIsDistinctFromMissingRecord(t *testing.T) {
	for _, state := range []string{"fresh", "reset", "deleted"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t, IdleExpirationConfig{})
			ctx, scope := t.Context(), privateScope()
			if state != "fresh" {
				row := f.create(t, scope, "previous")
				var err error
				if state == "reset" {
					err = f.s.ResetCurrent(ctx, scope)
				} else {
					err = f.store.Sessions().Delete(ctx, row.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			row, binding, err := f.s.CurrentBound(ctx, scope)
			if !errors.Is(err, ErrNoCurrentSession) || errors.Is(err, storage.ErrNotFound) || row != nil || binding != nil {
				t.Fatalf("current: row=%+v binding=%v err=%v", row, binding, err)
			}
			if f.s.CurrentBinding(scope) != nil {
				t.Fatal("empty current retained a binding")
			}
			if _, err := f.s.Rename(ctx, scope, "", "title"); !errors.Is(err, ErrNoCurrentSession) {
				t.Fatalf("default target: %v", err)
			}
			if _, err := f.s.Pin(ctx, scope, "missing"); !errors.Is(err, storage.ErrNotFound) || errors.Is(err, ErrNoCurrentSession) {
				t.Fatalf("explicit missing target: %v", err)
			}
			current, err := f.s.GetOrCreateCurrent(ctx, scope, "first message")
			if err != nil || current == nil {
				t.Fatalf("first message could not create a session: %v", err)
			}
		})
	}
}

type currentReadFailure struct {
	storage.SessionRepository
	err error
}

func (r currentReadFailure) Get(context.Context, string) (*storage.Session, error) {
	return nil, r.err
}

func TestCurrentReadFailureIsNotEmptyConversation(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := t.Context(), privateScope()
	f.create(t, scope, "current")
	original := f.s.CurrentBinding(scope)
	want := errors.New("database unavailable")
	f.s.deps.Sessions = currentReadFailure{SessionRepository: f.store.Sessions(), err: want}
	if _, _, err := f.s.CurrentBound(ctx, scope); !errors.Is(err, want) || errors.Is(err, ErrNoCurrentSession) {
		t.Fatalf("current read error = %v", err)
	}
	if _, err := f.s.GetOrCreateCurrent(ctx, scope, "message"); !errors.Is(err, want) {
		t.Fatalf("get or create error = %v", err)
	}
	if f.s.CurrentBinding(scope) != original {
		t.Fatal("database failure changed the binding")
	}
}

func TestActivateModeReplacesDeletedCurrent(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := t.Context(), privateScope()
	row := f.create(t, scope, "deleted")
	if err := f.store.Sessions().Delete(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	result, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat})
	if err != nil {
		t.Fatal(err)
	}
	if result.Session.ID == row.ID || result.Session.Mode != storage.SessionModeChat || result.AlreadyActive {
		t.Fatalf("activation result = %+v", result)
	}
}
