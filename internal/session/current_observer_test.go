package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/utils/fileops"
)

func TestBindingInvalidatesRollbackAtLifecycleBoundary(t *testing.T) {
	for _, action := range []string{"reset", "create", "resume", "fork", "idle", "delete", "cleanup", "missing"} {
		t.Run(action, func(t *testing.T) {
			svc, store := newTestService(t)
			ctx := context.Background()
			scope := Scope{ActorID: "user", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
			first, err := svc.Create(ctx, scope, CreateRequest{Title: "first"})
			if err != nil {
				t.Fatal(err)
			}
			manager := fileops.NewRollbackManager()
			_, binding, _ := svc.CurrentBound(ctx, scope)
			_, err = svc.BindingChanged().Connect(func(_ context.Context, event BindingChangedEvent) error { manager.Forget(event.Old); return nil }, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			lease, ok := manager.Session(binding)
			if !ok {
				t.Fatal("current binding was not accepted")
			}
			if _, err := svc.Resume(ctx, scope, first.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := lease.List(); err != nil {
				t.Fatalf("same session invalidated: %v", err)
			}
			otherScope := Scope{ActorID: "other", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
			other, err := svc.Create(ctx, otherScope, CreateRequest{Title: "other"})
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "reset":
				svc.ResetCurrent(ctx, scope)
			case "create":
				_, err = svc.Create(ctx, scope, CreateRequest{Title: "next"})
			case "resume":
				row := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID, Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "next"}
				if err = store.Sessions().Create(ctx, row); err == nil {
					_, err = svc.Resume(ctx, scope, row.ID)
				}
			case "fork":
				message := &storage.Message{SessionID: first.ID, Role: storage.RoleAssistant, Content: "answer"}
				if err = store.Messages().Append(ctx, message); err == nil {
					_, err = svc.Fork(ctx, scope, message.ID)
				}
			case "idle":
				first.UpdatedAt = time.Now().Add(-2 * time.Hour)
				if _, err = store.Sessions().Mutate(ctx, first.ID, func(latest *storage.Session) error { *latest = *first; return nil }); err == nil {
					var result ExpireIdleResult
					result, err = svc.ExpireIdleCurrent(ctx, ExpireIdleRequest{Scope: scope, Config: IdleExpirationConfig{PrivateUserTTLMinutes: 1}})
					if err == nil && !result.Expired {
						t.Fatal("session did not expire")
					}
				}
			case "delete":
				err = svc.Delete(ctx, scope, first.ID)
			case "cleanup":
				first.UpdatedAt = time.Now().Add(-2 * time.Hour)
				if _, err = store.Sessions().Mutate(ctx, first.ID, func(latest *storage.Session) error { *latest = *first; return nil }); err == nil {
					_, err = svc.CleanupExpired(ctx, time.Now().Add(-time.Hour))
				}
			case "missing":
				if err = store.Sessions().Delete(ctx, first.ID); err == nil {
					_, getErr := svc.Current(ctx, scope)
					if !errors.Is(getErr, storage.ErrNotFound) {
						t.Fatalf("missing current: %v", getErr)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lease.List(); !errors.Is(err, fileops.ErrRollbackExpired) {
				t.Fatalf("lease still valid after %s: %v", action, err)
			}
			_, otherBinding, _ := svc.CurrentBound(ctx, otherScope)
			if otherBinding.SessionID() != other.ID {
				t.Fatal("other current changed")
			}
			if _, ok := manager.Session(otherBinding); !ok {
				t.Fatal("other scope invalidated")
			}
			if action != "delete" && action != "cleanup" && action != "missing" {
				if _, err := svc.Resume(ctx, scope, first.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := lease.List(); !errors.Is(err, fileops.ErrRollbackExpired) {
					t.Fatal("old lease revived")
				}
				_, fresh, _ := svc.CurrentBound(ctx, scope)
				if _, ok := manager.Session(fresh); !ok {
					t.Fatal("fresh lease missing")
				}
			}
		})
	}
}

func TestBindingIgnoresFailedAndConditionalTransitions(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "user", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	var transitions []string
	_, _ = svc.BindingChanged().Connect(func(_ context.Context, e BindingChangedEvent) error {
		id := ""
		if e.New != nil {
			id = e.New.SessionID()
		}
		transitions = append(transitions, scope.Key()+"="+id)
		return nil
	}, signal.ConnectOptions{})
	row, err := svc.Create(ctx, scope, CreateRequest{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	svc.clearBinding(ctx, &Binding{scope: scope, sessionID: "not-current"}, ChangeReset)
	if _, err := svc.Resume(ctx, scope, "missing"); err == nil {
		t.Fatal("resume missing succeeded")
	}
	if len(transitions) != 1 {
		t.Fatalf("unexpected transitions: %v", transitions)
	}
	svc.ResetCurrent(ctx, scope)
	svc.ResetCurrent(ctx, scope)
	if len(transitions) != 2 || transitions[1] != scope.Key()+"=" {
		t.Fatalf("reset transitions: %v; row %s", transitions, row.ID)
	}
}
