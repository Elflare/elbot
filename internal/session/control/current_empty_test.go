package control

import (
	"context"
	"elbot/internal/operationresult"
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
					result, resetErr := f.s.ResetCurrent(ctx, scope)
					err = resetErr
					if err == nil && result.Status != operationresult.Success {
						t.Fatalf("reset: %+v", result)
					}
				} else {
					err = f.store.Sessions().Delete(ctx, row.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			current, err := f.s.CurrentBound(ctx, scope)
			if err != nil || current.Status != operationresult.NoCurrentSession || current.Data.Session != nil || current.Data.Binding != nil {
				t.Fatalf("current: %+v err=%v", current, err)
			}
			if f.s.CurrentBinding(scope) != nil {
				t.Fatal("empty current retained a binding")
			}
			if result, err := f.s.Rename(ctx, scope, "", "title"); err != nil || result.Status != operationresult.NoCurrentSession {
				t.Fatalf("default target: %+v %v", result, err)
			}
			if result, err := f.s.Pin(ctx, scope, "missing"); err != nil || result.Status != operationresult.NotFound {
				t.Fatalf("explicit missing target: %+v %v", result, err)
			}
			created, err := f.s.GetOrCreateCurrent(ctx, scope, "first message")
			if err != nil || created.Status != operationresult.Success || created.Data == nil {
				t.Fatalf("first message could not create a session: %+v %v", created, err)
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
	if result, err := f.s.CurrentBound(ctx, scope); !errors.Is(err, want) || result.Status != "" {
		t.Fatalf("current read error = %+v / %v", result, err)
	}
	if result, err := f.s.GetOrCreateCurrent(ctx, scope, "message"); !errors.Is(err, want) || result.Status != "" {
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
	if err != nil || result.Status != operationresult.Success {
		t.Fatalf("activate mode: %+v %v", result, err)
	}
	if result.Data.Session.ID == row.ID || result.Data.Session.Mode != storage.SessionModeChat || result.Data.AlreadyActive {
		t.Fatalf("activation result = %+v", result.Data)
	}
}
