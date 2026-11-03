package confirmation

import toolshare "elbot/internal/tool"

import (
	"context"
	commandpkg "elbot/internal/command"
	"elbot/internal/delivery"
	"elbot/internal/security"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil"
	"errors"

	"testing"
	"testing/synctest"
	"time"

	agentshare "elbot/internal/agent/share"

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
			identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
			row, err := sessions.Create(t.Context(), identity.Scope(testutil.WithCLIConversation(t.Context())), session.CreateRequest{Title: "confirmation"})
			if err != nil {
				t.Fatal(err)
			}
			turns := turn.NewSupervisor(testutil.IDSequence())
			defer turns.StopAll()
			ctx := testutil.WithCLIConversation(t.Context())
			execution := turn.NewExecution("confirmation")
			requestCtx := turn.WithAttempt(turn.WithExecution(ctx, execution), "attempt")
			if !turns.StartAttempt(row.ID, turn.Input{Text: "hello"}, execution, "attempt") || !turns.StartToolPhase(row.ID, "attempt") {
				t.Fatal("start tools")
			}
			coordinator := NewConfirmationCoordinator(ConfirmationCoordinatorDeps{CommandText: testCommandText, Sessions: sessions, Requests: request.NewManager(0, testutil.IDSequence()), Turns: turns, Commands: commandpkg.NewRouter([]string{"/"}), Identity: identity, Output: confirmationOutput{}, Policy: NewConfirmationPolicy(ConfirmationPolicyDeps{Identity: identity, UserConfirmationTimeout: time.Second})})

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
				turns := turn.NewSupervisor(testutil.IDSequence())
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

func (o confirmationOutput) SendAssistant(_ context.Context, text string, actions ...delivery.Action) (delivery.Receipt, error) {
	if o.texts != nil {
		o.texts <- text
	}
	if o.sent != nil {
		o.sent <- actions
	}
	if o.err != nil {
		return delivery.Receipt{}, o.err
	}
	return delivery.Receipt{}, nil
}

func TestConfirmationPromptCarriesExecutableActions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		turns := turn.NewSupervisor(testutil.IDSequence())
		execution := turn.NewExecution("run")
		turns.StartAttempt("s", turn.Input{}, execution, "a")
		turns.StartToolPhase("s", "a")
		ctx := turn.WithAttempt(turn.WithExecution(t.Context(), execution), "a")
		output := confirmationOutput{sent: make(chan []delivery.Action, 2), texts: make(chan string, 2)}
		commandText := func(name string, args ...string) string { return commandpkg.Format("!", name, args...) }
		coordinator := NewConfirmationCoordinator(ConfirmationCoordinatorDeps{CommandText: commandText, Turns: turns, Output: output, Policy: fixedWaitPolicy(time.Second)})
		result, err := coordinator.AwaitToolConfirmation(ctx, "s", llm.ToolCallRequest{ID: "call", Name: "shell", Arguments: "{}"}, toolshare.RiskAssessment{Level: security.RiskHigh}, "detail")
		if err != nil || !result.Stopped {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		actions := <-output.sent
		if len(output.texts) != 2 || len(output.sent) != 1 {
			t.Fatal("confirmation prompt or timeout notice missing")
		}
		<-output.texts
		if got := <-output.texts; got != "confirm timeout → stopped" {
			t.Fatalf("timeout notice=%q", got)
		}
		if got := <-output.sent; len(got) != 0 {
			t.Fatalf("timeout notice acquired actions: %+v", got)
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

func TestCronRiskConfirmationArgumentDetails(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{
			name: "create",
			args: `{"operation":"create","name":"drink","schedule_mode":"once","run_after_minutes":10,"trigger_mode":"direct","message":"喝水\n起来活动一下"}`,
			want: "cron_write [high]\nmessage: |\n  喝水\n  起来活动一下\nname: drink\noperation: create\nrun_after_minutes: 10\nschedule_mode: once\ntrigger_mode: direct",
		},
		{
			name: "update",
			args: `{"operation":"update","name":"drink","tool_list_names":[],"model_provider":"","model":"","enabled":false}`,
			want: "cron_write [high]\nenabled: false\nmodel: \"\"\nmodel_provider: \"\"\nname: drink\noperation: update\ntool_list_names:\n  []",
		},
		{
			name: "disable",
			args: `{"operation":"disable","name":"drink"}`,
			want: "cron_write [high]\nname: drink\noperation: disable",
		},
		{
			name: "delete",
			args: `{"operation":"delete","name":"drink"}`,
			want: "cron_write [high]\nname: drink\noperation: delete",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := riskConfirmationDetailText(turn.RiskConfirmation{
				ToolName: "cron_write", Risk: "high", Arguments: tc.args,
			})
			if got != tc.want {
				t.Fatalf("detail:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func testCommandText(name string, args ...string) string {
	return commandpkg.Format("/", name, args...)
}
