package confirmation

import toolshare "elbot/internal/tool"

import (
	"context"
	commandpkg "elbot/internal/command"
	"elbot/internal/delivery"
	"elbot/internal/security"
	"elbot/internal/storage/sqlite"
	"errors"
	"strings"

	"testing"
	"testing/synctest"
	"time"

	agentshare "elbot/internal/agent/share"

	"elbot/internal/events"

	"elbot/internal/llm"

	"elbot/internal/request"
	"elbot/internal/session"

	"elbot/internal/turn"
)

func TestConfirmationCoordinatorPreservesAutoConfirmationScope(t *testing.T) {
	for _, command := range []string{"/confirm extra", "/confirmtool extra", "/confirmall extra", "/reject reason", "/stop"} {
		t.Run(command, func(t *testing.T) {
			store, err := sqlite.New(t.Context(), ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sessions := newSessionSupervisor(t, store)
			defer sessions.Close(context.Background())
			identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Permission: security.DefaultPermission()})
			row, err := sessions.Create(t.Context(), identity.Scope(t.Context()), session.CreateRequest{Title: "confirmation"})
			if err != nil {
				t.Fatal(err)
			}
			turns := turn.NewSupervisor()
			defer turns.StopAll()
			ctx := t.Context()
			execution := turn.NewExecution("confirmation")
			requestCtx := turn.WithAttempt(turn.WithExecution(ctx, execution), "attempt")
			if !turns.StartAttempt(row.ID, turn.Input{Text: "hello"}, execution, "attempt") || !turns.StartToolPhase(row.ID, "attempt") {
				t.Fatal("start tools")
			}
			coordinator := NewConfirmationCoordinator(ConfirmationCoordinatorDeps{CommandText: testCommandText, Sessions: sessions, Requests: request.NewManager(0), Turns: turns, Commands: commandpkg.NewRouter([]string{"/"}), Identity: identity, Output: confirmationOutput{}, Policy: NewConfirmationPolicy(ConfirmationPolicyDeps{Identity: identity, UserConfirmationTimeout: time.Second})})

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
				allowed := command == "/confirm extra" || command == "/confirmtool extra" || command == "/confirmall extra"
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

func TestConfirmationTerminationAndPromptFailure(t *testing.T) {
	for _, mode := range []string{"expired", "canceled", "prompt_failed"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				turns := turn.NewSupervisor()
				execution := turn.NewExecution("run")
				turns.StartAttempt("s", turn.Input{}, execution, "a")
				turns.StartToolPhase("s", "a")
				ctx, cancel := context.WithCancel(turn.WithAttempt(turn.WithExecution(t.Context(), execution), "a"))
				defer cancel()
				output := confirmationOutput{}
				if mode == "canceled" {
					cancel()
				}
				if mode == "prompt_failed" {
					output.err = errors.New("delivery failed")
				}
				coordinator := NewConfirmationCoordinator(ConfirmationCoordinatorDeps{CommandText: testCommandText, Turns: turns, Output: output, Policy: fixedWaitPolicy(time.Second)})
				result, err := coordinator.AwaitToolConfirmation(ctx, "s", llm.ToolCallRequest{ID: "call", Name: "danger", Arguments: "{}"}, toolshare.RiskAssessment{Level: security.RiskHigh}, "detail")
				if !result.Stopped || result.Allowed || !errors.Is(err, output.err) {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if turns.Snapshot("s").Phase != turn.PhaseStopping {
					t.Fatal("confirmation failure did not stop attempt")
				}
				turns.FinishAttempt("s", "a")
			})
		})
	}
}

type fixedWaitPolicy time.Duration

func (p fixedWaitPolicy) WaitTimeout(context.Context) time.Duration { return time.Duration(p) }

type confirmationOutput struct {
	err   error
	sent  chan []delivery.Action
	texts chan string
}

func (confirmationOutput) SendChat(context.Context, string) error { return nil }
func (o confirmationOutput) SendAssistant(_ context.Context, text string, actions ...delivery.Action) (delivery.Receipt, events.LogResult, error) {
	if o.texts != nil {
		o.texts <- text
	}
	if o.sent != nil {
		o.sent <- actions
	}
	if o.err != nil {
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultFailed}, o.err
	}
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func TestConfirmationPromptCarriesExecutableActions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		turns := turn.NewSupervisor()
		execution := turn.NewExecution("run")
		turns.StartAttempt("s", turn.Input{}, execution, "a")
		turns.StartToolPhase("s", "a")
		ctx := turn.WithAttempt(turn.WithExecution(t.Context(), execution), "a")
		output := confirmationOutput{sent: make(chan []delivery.Action, 1), texts: make(chan string, 1)}
		commandText := func(name string, args ...string) string { return commandpkg.Format("!", name, args...) }
		coordinator := NewConfirmationCoordinator(ConfirmationCoordinatorDeps{CommandText: commandText, Turns: turns, Output: output, Policy: fixedWaitPolicy(time.Second)})
		result, err := coordinator.AwaitToolConfirmation(ctx, "s", llm.ToolCallRequest{ID: "call", Name: "shell", Arguments: "{}"}, toolshare.RiskAssessment{Level: security.RiskHigh}, "detail")
		if err != nil || !result.Stopped {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		actions := <-output.sent
		text := <-output.texts
		if !strings.Contains(text, "!c 本次") || !strings.Contains(text, "!detail 重置计时") || strings.Contains(text, "/confirm") {
			t.Fatal(text)
		}
		want := []string{"detail", "confirm", "confirmtool", "confirmall", "reject", "stop"}
		if len(actions) != len(want) {
			t.Fatalf("actions=%#v", actions)
		}
		for i, name := range want {
			if actions[i].ID != name || actions[i].Label == "" || actions[i].Command != "!"+name {
				t.Fatalf("action=%#v", actions[i])
			}
		}
		turns.FinishAttempt("s", "a")
	})
}

func testCommandText(name string, args ...string) string {
	return commandpkg.Format("/", name, args...)
}
