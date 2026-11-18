package execution

import (
	"context"
	"testing"

	"elbot/internal/agent/dialogue"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/operationresult"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"elbot/internal/turn"
)

func TestEnterTurnAfterForegroundActivation(t *testing.T) {
	store := newTestStore(t)
	execution := turn.NewExecution("run")
	sessions := newSessionSupervisor(t, store, func(deps *session.SupervisorDeps) {
		deps.IsSuperadmin = func(session.Scope) bool { return true }
		deps.AdoptForeground = func(ctx context.Context, row *storage.Session, binding *session.Binding) {
			execution.Adopt(session.WithBinding(ctx, binding), *row)
		}
	})
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
	coordinator := NewExecutionCoordinator(t.Context(), ExecutionCoordinatorDeps{View: dialogue.ExecutionView{}, Sessions: sessions, SessionRows: store.Sessions(), Identity: identity})
	ctx := testutil.WithCLIConversation(t.Context())
	scope := identity.Scope(ctx)
	background, err := sessions.PrepareBackground(ctx, scope, session.BackgroundRequest{TaskType: "cron", TaskID: "job"})
	if err != nil || background.Status != operationresult.Success {
		t.Fatal(background, err)
	}
	stale := *background.Data
	promoted, err := sessions.Resume(ctx, scope, stale.ID)
	if err != nil || promoted.Status != operationresult.Success {
		t.Fatal(promoted, err)
	}
	task := testutil.TaskContext(t.Context(), "cron")
	withoutOwner := stale
	_, result, _, err := coordinator.enterTurn(turn.WithExecution(task, turn.NewExecution("unowned")), &withoutOwner)
	if err != nil || result.Status != operationresult.Conflict {
		t.Fatalf("without foreground identity: %+v, %v", result, err)
	}
	locked, result, release, err := coordinator.enterTurn(turn.WithExecution(task, execution), &stale)
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(result, err)
	}
	defer release()
	binding, ok := session.BindingFromContext(locked)
	if !ok || binding.SessionID() != stale.ID || session.IsBackground(&stale) || identity.Scope(locked).Key() != scope.Key() {
		t.Fatal("foreground binding or identity not restored")
	}
}
