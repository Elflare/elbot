package confirmation

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/operationresult"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil"
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
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
	c = NewConfirmationCoordinator(ConfirmationCoordinatorDeps{CommandText: testCommandText, Sessions: s, Identity: identity})
	scope := identity.Scope(testutil.WithCLIConversation(t.Context()))
	created, err := s.Create(t.Context(), scope, session.CreateRequest{})
	if err != nil || created.Status != operationresult.Success {
		t.Fatalf("create: %+v %v", created, err)
	}
	row := created.Data
	c.setSessionAutoConfirmed(row.ID)
	c.setToolAutoConfirmed(row.ID, "shell")
	c.setSessionAutoConfirmed("other")
	_, err = s.ObserveBindingChanged(func(context.Context, session.BindingChangedEvent) {}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if reset, err := s.ResetCurrent(t.Context(), scope); err != nil || reset.Status != operationresult.Success {
		t.Fatalf("reset: %+v %v", reset, err)
	}
	if c.isSessionAutoConfirmed(row.ID) || c.isToolAutoConfirmed(row.ID, "shell") || !c.isSessionAutoConfirmed("other") {
		t.Fatal("retirement released the wrong authorization")
	}
	if resumed, err := s.Resume(t.Context(), scope, row.ID); err != nil || resumed.Status != operationresult.Success {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	if c.isSessionAutoConfirmed(row.ID) || c.isToolAutoConfirmed(row.ID, "shell") {
		t.Fatal("resuming history restored old authorization")
	}
}
