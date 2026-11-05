package execution

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"elbot/internal/agent/dialogue"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"elbot/internal/turn"
)

type failingWorkflowNotice struct {
	backgroundTurnOutput
	text string
	err  error
}

func (o failingWorkflowNotice) Send(_ context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	if event.Text == o.text {
		return delivery.Receipt{}, o.err
	}
	return delivery.Receipt{}, nil
}
func (failingWorkflowNotice) Preview(context.Context, ...string) {}

type shortAppendWait struct{}

func (shortAppendWait) WaitTimeout(context.Context) time.Duration { return time.Millisecond }

type noticeTestContexts struct{ ExecutionContexts }

func (noticeTestContexts) ReachedCompactThreshold(context.Context, *llm.Usage, modelmgr.Selection) bool {
	return true
}
func (noticeTestContexts) RecordTurn(context.Context, string, *llm.Usage) error { return nil }

func TestWorkflowNoticeFailureIsLoggedAndHandled(t *testing.T) {
	for _, tc := range []struct{ name, text, event string }{
		{"append", "append timeout → dropped · stopped", "append_timeout_notice_failed"},
		{"compact", "compact → next request", "compact_notice_failed"},
	} {
		for _, failure := range []error{errors.New("notice transport failed"), context.Canceled} {
			t.Run(tc.name+"/"+failure.Error(), func(t *testing.T) {
				store := newTestStore(t)
				sessions := newSessionSupervisor(t, store)
				identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
				ctx := testutil.WithCLIConversation(t.Context())
				row, err := sessions.GetOrCreateCurrent(ctx, identity.Scope(ctx), "notice")
				if err != nil {
					t.Fatal(err)
				}
				turns := turn.NewSupervisor(testutil.IDSequence())
				execution := turn.NewExecution("run")
				ctx = turn.WithAttempt(turn.WithExecution(ctx, execution), "attempt")
				ctx = contextinfo.WithExecution(ctx, contextinfo.Execution{SessionID: row.ID, RunID: "run", Attempt: "attempt", RequestID: "request"})
				turns.StartAttempt(row.ID, turn.Input{}, execution, "attempt")
				defer turns.FinishAttempt(row.ID, "attempt")
				view := dialogue.ExecutionView{Sessions: store.Sessions()}
				out := failingWorkflowNotice{text: tc.text, err: failure}
				c := NewExecutionCoordinator(ctx, ExecutionCoordinatorDeps{
					Sessions: sessions, SessionRows: store.Sessions(), Turns: turns, Requests: request.NewManager(testutil.IDSequence()),
					View: view, Identity: identity, Output: out, Contexts: noticeTestContexts{}, WaitPolicy: shortAppendWait{},
					Status:           NewStatusRecorder(StatusRecorderDeps{View: view, Turns: turns}),
					SelectionForTurn: func(context.Context, *storage.Session) modelmgr.Selection { return modelmgr.Selection{} },
				})
				defer c.Close(context.Background())
				logs := make(chan events.LogRecord, 1)
				connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
					if record.EventName == tc.event {
						logs <- record
					}
					return nil
				}, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Close(context.Background())
				if tc.name == "append" {
					_, disposition, err := c.AcceptInput(ctx, row, "pending")
					if err != nil || disposition != agentshare.InputHandled {
						t.Fatalf("disposition=%v err=%v", disposition, err)
					}
				} else {
					turns.StartToolPhase(row.ID, "attempt")
					turns.AppendPendingInput(row.ID, turn.Input{Text: "pending"})
					pending, err := c.finishCompletedTurn(ctx, row, out, agentshare.TurnResult{})
					if err != nil || pending.Text != "pending" {
						t.Fatalf("pending=%+v err=%v", pending, err)
					}
				}
				select {
				case record := <-logs:
					level := slog.LevelWarn
					if failure == context.Canceled {
						level = slog.LevelInfo
					}
					if record.Level != level || record.ResultStatus != events.LogStatus(failure) || record.Content != failure.Error() || record.Identity != events.LogIdentityFromContext(ctx) {
						t.Fatalf("incorrect notice log: %+v", record)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("notice failure was not logged")
				}
			})
		}
	}
}
