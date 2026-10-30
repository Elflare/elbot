package confirmation

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage/sqlite"
	"errors"
	"testing"
)

func TestRetiredSessionDoesNotRestoreAutomaticConfirmation(t *testing.T) {
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var c *ConfirmationCoordinator
	s := newSessionSupervisor(t, store, func(deps *session.SupervisorDeps) { deps.ReleaseSession = func(id string) { c.ForgetSession(id) } })
	defer s.Close(context.Background())
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Permission: security.DefaultPermission()})
	c = NewConfirmationCoordinator(ConfirmationCoordinatorDeps{CommandText: testCommandText, Sessions: s, Identity: identity})
	scope := identity.Scope(t.Context())
	row, err := s.Create(t.Context(), scope, session.CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	c.setSessionAutoConfirmed(row.ID)
	c.setToolAutoConfirmed(row.ID, "shell")
	c.setSessionAutoConfirmed("other")
	_, err = s.ObserveBindingChanged(func(context.Context, session.BindingChangedEvent) error { return errors.New("observer failed") }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetCurrent(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if c.isSessionAutoConfirmed(row.ID) || c.isToolAutoConfirmed(row.ID, "shell") || !c.isSessionAutoConfirmed("other") {
		t.Fatal("retirement released the wrong authorization")
	}
	if _, err := s.Resume(t.Context(), scope, row.ID); err != nil {
		t.Fatal(err)
	}
	if c.isSessionAutoConfirmed(row.ID) || c.isToolAutoConfirmed(row.ID, "shell") {
		t.Fatal("resuming history restored old authorization")
	}
}
