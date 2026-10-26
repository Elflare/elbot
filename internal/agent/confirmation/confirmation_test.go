package confirmation

import toolshare "elbot/internal/tool"

import (
	"context"
	commandpkg "elbot/internal/command"
	"elbot/internal/delivery"
	"elbot/internal/security"
	"elbot/internal/storage/sqlite"

	"testing"
	"time"

	agentshare "elbot/internal/agent/share"

	"elbot/internal/events"

	"elbot/internal/llm"

	"elbot/internal/request"
	"elbot/internal/session"

	"elbot/internal/turn"
)

func TestConfirmationCoordinatorPreservesAutoConfirmationScope(t *testing.T) {
	for _, command := range []string{"/confirmtool extra", "/confirmall extra", "/reject reason", "/stop"} {
		t.Run(command, func(t *testing.T) {
			store, err := sqlite.New(t.Context(), ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sessions := session.NewService(store)
			defer sessions.Close(context.Background())
			identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Permission: security.DefaultPermission()})
			row, err := sessions.Create(t.Context(), identity.Scope(t.Context()), session.CreateRequest{Title: "confirmation"})
			if err != nil {
				t.Fatal(err)
			}
			turns := turn.NewManager()
			defer turns.StopAll()
			ctx := t.Context()
			execution := turn.NewExecution("confirmation")
			requestCtx := turn.WithAttempt(turn.WithExecution(ctx, execution), "attempt")
			if !turns.StartExecution(row.ID, turn.Input{Text: "hello"}, execution, "attempt") || !turns.StartToolPhase(row.ID, "attempt") {
				t.Fatal("start tools")
			}
			coordinator := NewConfirmationCoordinator(ConfirmationCoordinatorDeps{Sessions: sessions, Requests: request.NewManager(0), Turns: turns, Commands: commandpkg.NewRouter([]string{"/"}), Identity: identity, Output: confirmationOutput{}, Policy: NewConfirmationPolicy(ConfirmationPolicyDeps{Identity: identity, UserConfirmationTimeout: time.Second})})

			done := make(chan toolshare.ConfirmResult, 1)
			go func() {
				result, _ := coordinator.AwaitToolConfirmation(requestCtx, row.ID, llm.ToolCallRequest{ID: "call", Name: "danger", Arguments: "{}"}, toolshare.RiskAssessment{Level: security.RiskHigh}, "detail")
				done <- result
			}()
			deadline := time.Now().Add(time.Second)
			for turns.Snapshot(row.ID).Phase != turn.PhaseAwaitRiskConfirm && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if turns.Snapshot(row.ID).Phase != turn.PhaseAwaitRiskConfirm {
				t.Fatal("confirmation not pending")
			}
			if err := coordinator.SubmitResponse(ctx, row.ID, command); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				allowed := command == "/confirmtool extra" || command == "/confirmall extra"
				if result.Allowed != allowed || result.Stopped != (command == "/stop") || (allowed && result.Extra != "extra") {
					t.Fatalf("confirmation result=%+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("confirmation did not finish")
			}
			if coordinator.isSessionAutoConfirmed(row.ID) != (command == "/confirmall extra") || coordinator.isToolAutoConfirmed(row.ID, "danger") != (command == "/confirmtool extra") {
				t.Fatal("wrong automatic confirmation scope")
			}
			if coordinator.isSessionAutoConfirmed("another-session") || coordinator.isToolAutoConfirmed("another-session", "danger") || coordinator.isToolAutoConfirmed(row.ID, "another-tool") {
				t.Fatal("automatic confirmation escaped its scope")
			}
		})
	}
}

type confirmationOutput struct{}

func (confirmationOutput) SendChat(context.Context, string) error { return nil }
func (confirmationOutput) SendAssistant(context.Context, string) (delivery.Receipt, events.LogResult, error) {
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}
