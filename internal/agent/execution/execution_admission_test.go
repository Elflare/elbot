package execution

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/agent/dialogue"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/signal"
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
	coordinator := NewExecutionCoordinator(t.Context(), ExecutionCoordinatorDeps{View: dialogue.ExecutionView{}, Sessions: sessions, SessionRows: store, Identity: identity})
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

type rejectedReportDialogue struct {
	ExecutionDialogue
	turns   *turn.Supervisor
	failure error
	checks  int
}

func (d *rejectedReportDialogue) CheckSelection(modelmgr.Selection) (operationresult.Result[struct{}], error) {
	d.checks++
	if d.checks == 2 {
		return operationresult.Result[struct{}]{Status: operationresult.Rejected}, nil
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (d *rejectedReportDialogue) PrepareTurn(_ context.Context, in agentshare.TurnInput) (agentshare.PreparedTurn, error) {
	d.turns.AppendPendingInput(in.Session.ID, turn.Input{Reports: []*storage.Message{{Role: llm.RoleAssistant}, {Role: llm.RoleTool}}})
	return nil, d.failure
}

func TestAsyncReportRejectionPreservesFirstError(t *testing.T) {
	ctx := testutil.WithCLIConversation(t.Context())
	store := newTestStore(t)
	sessions := newSessionSupervisor(t, store)
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
	created, err := sessions.Create(ctx, identity.Scope(ctx), session.CreateRequest{})
	if err != nil || created.Status != operationresult.Success {
		t.Fatalf("create: %+v %v", created, err)
	}
	turns := usageTestTurns{turn.NewSupervisor(testutil.IDSequence())}
	d := &rejectedReportDialogue{turns: turns.Supervisor, failure: errors.New("original failure")}
	view := dialogue.ExecutionView{Sessions: store}
	changed := signal.New[StatusChangedEvent, signal.Empty]("report rejection")
	defer changed.Close(context.Background())
	c := NewExecutionCoordinator(ctx, ExecutionCoordinatorDeps{
		Sessions: sessions, SessionRows: store, Turns: turns, Requests: request.NewManager(testutil.IDSequence()), Identity: identity, View: view,
		Status: NewStatusRecorder(StatusRecorderDeps{View: view, Turns: turns, Changed: changed}), Dialogue: d,
		SelectionForTurn: func(context.Context, *storage.Session) modelmgr.Selection {
			return modelmgr.Selection{Client: &fakeLLM{}, Origin: llm.Origin{Provider: "test", APIType: llm.APITypeChat}}
		},
	})
	defer c.Close(context.Background())
	_, _, err = c.Run(ctx, created.Data, "input", backgroundTurnOutput{})
	if d.checks != 2 || !errors.Is(err, d.failure) {
		t.Fatalf("selection checks=%d, got %v; want original failure after report rejection", d.checks, err)
	}
}
