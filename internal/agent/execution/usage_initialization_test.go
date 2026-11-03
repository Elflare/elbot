package execution

import (
	"context"
	"elbot/internal/testutil"
	"errors"
	"testing"

	"elbot/internal/agent/dialogue"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type usageTestTurns struct{ *turn.Supervisor }

func (usageTestTurns) CanCompact(string, *turn.Execution) bool { return false }

type usageTestRequests struct {
	*request.Manager
	cancelOnStart bool
	started       int
}

func (r *usageTestRequests) Start(ctx context.Context, in request.StartRequest) (request.Request, context.Context, func(), error) {
	r.started++
	info, runCtx, done, err := r.Manager.Start(ctx, in)
	if err != nil {
		return info, runCtx, done, err
	}
	if r.cancelOnStart {
		r.Cancel(info.ID)
	}
	return info, runCtx, done, nil
}

type usageTestStatus struct {
	*StatusRecorder
	requests *usageTestRequests
	stopping <-chan struct{}
}

func (s usageTestStatus) Usage(row *storage.Session) (*llm.Usage, error) {
	// If cancellation is already possible, let its observer finish first. This
	// makes the lost-usage interleaving deterministic without timing sleeps.
	if s.requests.cancelOnStart && len(s.requests.ListBySession(row.ID)) != 0 {
		<-s.stopping
	}
	return s.StatusRecorder.Usage(row)
}

type usageTestDialogue struct {
	ExecutionDialogue
	t        *testing.T
	stopping <-chan struct{}
}

func (usageTestDialogue) CheckSelection(modelmgr.Selection) error { return nil }

func (d usageTestDialogue) PrepareTurn(context.Context, agentshare.TurnInput) (agentshare.PreparedTurn, error) {
	if d.stopping == nil {
		d.t.Fatal("dialogue started despite invalid durable usage")
	}
	<-d.stopping
	return nil, nil
}

func TestExecutionRestoresUsageBeforeCancellationAndCleansUpFailure(t *testing.T) {
	for _, tc := range []struct {
		name, metadata  string
		cancel          bool
		allocationFails bool
	}{
		{"immediate cancellation", `{"last_usage":{"TotalTokens":42}}`, true, false},
		{"invalid durable usage", `{"last_usage":"invalid"}`, false, false},
		{"request allocation failure", `{}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			sessions := newSessionSupervisor(t, store)
			turns := usageTestTurns{turn.NewSupervisor(testutil.IDSequence())}
			requests := &usageTestRequests{Manager: request.NewManager(0, testutil.IDSequence()), cancelOnStart: tc.cancel}
			if tc.allocationFails {
				requests.Manager = request.NewManager(0, func(context.Context) (int64, error) { return 0, errors.New("reserve failed") })
			}
			view := dialogue.ExecutionView{Sessions: store.Sessions()}
			changed := signal.New[StatusChangedEvent, signal.Empty]("usage-initialization")
			defer changed.Close(context.Background())
			recorder := NewStatusRecorder(StatusRecorderDeps{View: view, Turns: turns, Changed: changed})
			var stopping chan struct{}
			if tc.cancel {
				stopping = make(chan struct{})
				_, err := changed.Observe(func(_ context.Context, event StatusChangedEvent) error {
					if event.Snapshot.Phase == runtimestatus.PhaseStopping {
						usage := event.Snapshot.Usage
						if usage == nil || usage.TotalTokens == nil || *usage.TotalTokens != 42 {
							t.Errorf("cancellation published without durable usage: %+v", usage)
						}
						close(stopping)
					}
					return nil
				}, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
			row, err := sessions.Create(t.Context(), identity.Scope(testutil.WithCLIConversation(t.Context())), session.CreateRequest{Metadata: tc.metadata})
			if err != nil {
				t.Fatal(err)
			}
			c := NewExecutionCoordinator(t.Context(), ExecutionCoordinatorDeps{
				Sessions: sessions, SessionRows: store.Sessions(), Turns: turns, Requests: requests,
				Identity: identity, View: view,
				Status:   usageTestStatus{recorder, requests, stopping},
				Dialogue: usageTestDialogue{t: t, stopping: stopping},
				SelectionForTurn: func(context.Context, *storage.Session) modelmgr.Selection {
					return modelmgr.Selection{Client: &fakeLLM{}, Origin: llm.Origin{Provider: "test", APIType: llm.APITypeChat}}
				},
			})
			defer c.Close(context.Background())
			err = c.Run(testutil.WithCLIConversation(t.Context()), row, "input", backgroundTurnOutput{})
			if tc.cancel {
				usage := recorder.Snapshot(row.ID).Usage
				if usage == nil || usage.TotalTokens == nil || *usage.TotalTokens != 42 {
					t.Fatalf("durable usage lost: %+v", usage)
				}
			} else if err == nil || (!tc.allocationFails && requests.started != 0) || (tc.allocationFails && requests.started != 1) {
				t.Fatalf("failed initialization: err=%v requests started=%d", err, requests.started)
			}
			if turns.Snapshot(row.ID).Phase != turn.PhaseIdle || len(requests.List()) != 0 {
				t.Fatal("execution left active Turn or Request after exit")
			}
			_, release, err := sessions.EnterActivation(t.Context(), identity.Scope(testutil.WithCLIConversation(t.Context())), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
}
