package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"elbot/internal/storage"
	"elbot/internal/utils/fileops"
)

func TestCurrentObserverInvalidatesRollbackAtLifecycleBoundary(t *testing.T) {
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
			svc.SetCurrentObserver(manager.SetCurrent) // Also initializes already-active sessions.
			lease, ok := manager.Session(scope.Key(), first.ID)
			if !ok {
				t.Fatal("observer did not replay current")
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
				svc.ResetCurrent(scope)
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
				if err = store.Sessions().Update(ctx, first); err == nil {
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
				if err = store.Sessions().Update(ctx, first); err == nil {
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
			if _, ok := manager.Session(otherScope.Key(), other.ID); !ok {
				t.Fatal("other scope invalidated")
			}
			if action != "delete" && action != "cleanup" && action != "missing" {
				if _, err := svc.Resume(ctx, scope, first.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := lease.List(); !errors.Is(err, fileops.ErrRollbackExpired) {
					t.Fatal("old lease revived")
				}
				if _, ok := manager.Session(scope.Key(), first.ID); !ok {
					t.Fatal("fresh lease missing")
				}
			}
		})
	}
}

func TestCurrentObserverIgnoresFailedAndConditionalTransitions(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "user", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	var transitions []string
	svc.SetCurrentObserver(func(key, id string) { transitions = append(transitions, key+"="+id) })
	row, err := svc.Create(ctx, scope, CreateRequest{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	svc.clearCurrentIf(scope, "not-current")
	if _, err := svc.Resume(ctx, scope, "missing"); err == nil {
		t.Fatal("resume missing succeeded")
	}
	if len(transitions) != 1 {
		t.Fatalf("unexpected transitions: %v", transitions)
	}
	svc.ResetCurrent(scope)
	svc.ResetCurrent(scope)
	if len(transitions) != 2 || transitions[1] != scope.Key()+"=" {
		t.Fatalf("reset transitions: %v; row %s", transitions, row.ID)
	}
}
