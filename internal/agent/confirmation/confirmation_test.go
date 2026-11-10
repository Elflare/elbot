package confirmation

import toolshare "elbot/internal/tool"

import (
	"context"
	commandpkg "elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/security"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil"
	"encoding/json"
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
	for _, command := range []string{"/confirm extra", "/confirmtool extra", "/confirmall extra", "/reject reason"} {
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
			coordinator := NewConfirmationCoordinator(ConfirmationCoordinatorDeps{CommandText: testCommandText, Sessions: sessions, Requests: request.NewManager(testutil.IDSequence()), Turns: turns, Identity: identity, Output: confirmationOutput{}, Policy: NewConfirmationPolicy(ConfirmationPolicyDeps{Identity: identity, UserConfirmationTimeout: time.Second})})

			done := make(chan toolshare.ConfirmResult, 1)
			go func() {
				result, _ := coordinator.AwaitToolConfirmation(requestCtx, row.ID, llm.ToolCallRequest{ID: "call", Name: "danger", Arguments: "{}"}, toolshare.RiskAssessment{Level: security.RiskHigh}, []toolshare.ConfirmationDetailBlock{{Kind: toolshare.ConfirmationDetailText, Content: "detail"}})
				done <- result
			}()
			deadline := time.Now().Add(time.Second)
			for turns.Snapshot(row.ID).Phase != turn.PhaseAwaitRiskConfirm && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if turns.Snapshot(row.ID).Phase != turn.PhaseAwaitRiskConfirm {
				t.Fatal("confirmation not pending")
			}
			if _, ok := turns.PendingRiskConfirmation(row.ID); !ok {
				t.Fatal("risk confirmation missing")
			}
			scope, decision, extra := "current", "approve", "extra"
			if command == "/confirmtool extra" {
				scope = "tool"
			} else if command == "/confirmall extra" {
				scope = "all"
			} else if command == "/reject reason" {
				decision, extra = "reject", "reason"
			}
			response, err := coordinator.Respond(ctx, control.RespondConfirmationRequest{Decision: decision, Scope: scope, Extra: extra})
			if err != nil {
				t.Fatal(err)
			}
			if !response.Accepted {
				t.Fatalf("confirmation response=%+v", response)
			}
			duplicate, err := coordinator.Respond(ctx, control.RespondConfirmationRequest{Decision: decision, Scope: scope, Extra: extra})
			if err != nil || duplicate.Accepted {
				t.Fatalf("duplicate confirmation response=%+v error=%v", duplicate, err)
			}
			select {
			case result := <-done:
				allowed := command != "/reject reason"
				wantExtra := extra
				if !allowed {
					wantExtra = ""
				}
				if result.Allowed != allowed || result.Stopped || (allowed && result.Extra != wantExtra) {
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
				result, err := coordinator.AwaitToolConfirmation(ctx, "s", llm.ToolCallRequest{ID: "call", Name: "danger", Arguments: "{}"}, toolshare.RiskAssessment{Level: security.RiskHigh}, []toolshare.ConfirmationDetailBlock{{Kind: toolshare.ConfirmationDetailText, Content: "detail"}})
				if !result.Stopped || result.Allowed || !errors.Is(err, output.err) {
					t.Fatalf("err=%v", err)
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
	err    error
	sent   chan []delivery.Action
	texts  chan string
	blocks chan []delivery.ContentBlock
}

func (o confirmationOutput) Send(_ context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	if o.texts != nil {
		o.texts <- event.Text
	}
	if o.sent != nil {
		o.sent <- event.Actions
	}
	if o.blocks != nil {
		o.blocks <- event.Blocks
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
		output := confirmationOutput{sent: make(chan []delivery.Action, 2), texts: make(chan string, 2), blocks: make(chan []delivery.ContentBlock, 2)}
		commandText := func(name string, args ...string) string { return commandpkg.Format("!", name, args...) }
		coordinator := NewConfirmationCoordinator(ConfirmationCoordinatorDeps{CommandText: commandText, Turns: turns, Output: output, Policy: fixedWaitPolicy(time.Second)})
		result, err := coordinator.AwaitToolConfirmation(ctx, "s", llm.ToolCallRequest{ID: "call", Name: "shell", Arguments: "{}"}, toolshare.RiskAssessment{Level: security.RiskHigh}, []toolshare.ConfirmationDetailBlock{{Kind: toolshare.ConfirmationDetailText, Content: "detail"}})
		if err != nil || !result.Stopped {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		actions := <-output.sent
		blocks := <-output.blocks
		if len(blocks) < 3 || blocks[0].Kind != delivery.ContentText || blocks[0].Content != "shell [high]: confirm" || blocks[1].Kind != delivery.ContentText || blocks[1].Content != "detail" || blocks[len(blocks)-1].Kind != delivery.ContentText {
			t.Fatalf("confirmation blocks=%#v", blocks)
		}
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
		if got := <-output.blocks; len(got) != 0 {
			t.Fatalf("timeout notice acquired blocks: %+v", got)
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

func TestConfirmationDetailsAppendsArgumentsObject(t *testing.T) {
	details, err := confirmationDetailsWithArguments([]toolshare.ConfirmationDetailBlock{{Kind: toolshare.ConfirmationDetailText, Content: "preview"}}, `{"operation":"write","count":2}`)
	if err != nil || len(details) != 2 || details[0].Kind != toolshare.ConfirmationDetailText || details[1].Kind != toolshare.ConfirmationDetailArguments {
		t.Fatalf("details=%+v", details)
	}
	args, ok := details[1].Content.(map[string]any)
	if !ok || args["operation"] != "write" || args["count"] != json.Number("2") {
		t.Fatalf("arguments content=%#v", details[1].Content)
	}
	if got := delivery.ContentBlocksText(details); got != "preview\n\n```json\n{\n  \"count\": 2,\n  \"operation\": \"write\"\n}\n```" {
		t.Fatalf("plain-text confirmation=%q", got)
	}
}

func TestConfirmationDetailsRejectInvalidArgumentJSON(t *testing.T) {
	for _, args := range []string{"not-json", "[]", `{} {}`} {
		if _, err := confirmationDetailsWithArguments(nil, args); err == nil {
			t.Errorf("accepted invalid arguments %q", args)
		}
	}
}

func testCommandText(name string, args ...string) string {
	return commandpkg.Format("/", name, args...)
}

func TestMissingRiskConfirmationReturnsRejection(t *testing.T) {
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions := newSessionSupervisor(t, store)
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
	ctx := testutil.WithCLIConversation(t.Context())
	_, err = sessions.Create(ctx, identity.Scope(ctx), session.CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turns := turn.NewSupervisor(testutil.IDSequence())
	t.Cleanup(turns.StopAll)
	c := NewConfirmationCoordinator(ConfirmationCoordinatorDeps{Sessions: sessions, Turns: turns, Identity: identity})
	result, err := c.Respond(ctx, control.RespondConfirmationRequest{Decision: "approve", Scope: "current"})
	if err != nil || result.Accepted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
