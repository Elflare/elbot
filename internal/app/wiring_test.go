package app

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/chatinfo"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"elbot/internal/turn"
)

func TestBuildAgentInstallsExecutionParticipants(t *testing.T) {
	ctx := context.Background()
	req, _, _ := runtimeAssemblyFixture(t)
	services, err := buildSharedServices(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	hooks := hook.NewManager()
	if _, err := buildAgent(t.Context(), req.Foundation, req.Platforms, services, &builtin.Runtime{Registry: tool.NewRegistry()}, hooks, nil); err != nil {
		t.Fatal(err)
	}
	scope := session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	front, err := services.Sessions.GetOrCreateCurrent(ctx, scope, "front")
	if err != nil {
		t.Fatal(err)
	}
	services.Turns.StartLLM(front.ID, "busy")
	if _, err := services.Sessions.Create(ctx, scope, session.CreateRequest{Title: "replacement"}); !errors.Is(err, session.ErrSessionBusy) {
		t.Fatalf("busy current session replaced: %v", err)
	}
	services.Turns.StopSession(front.ID)
	backgroundScope := scope
	backgroundScope.PlatformScopeID = "cron:assembly"
	row, err := services.Sessions.PrepareBackground(ctx, backgroundScope, session.BackgroundRequest{Kind: "cron", Name: "assembly"})
	if err != nil {
		t.Fatal(err)
	}
	execution := turn.NewExecution("assembly")
	if !services.Turns.StartExecution(row.ID, turn.Input{}, execution, "attempt") {
		t.Fatal("could not start background execution")
	}
	if _, err := services.Sessions.Resume(ctx, scope, row.ID); err != nil {
		t.Fatal(err)
	}
	if execution.Foreground() == nil {
		t.Fatal("session activation did not adopt the running execution")
	}
	services.Turns.StopSession(row.ID)

	called := 0
	if err := hooks.Register(hook.Registration{
		Point: hook.PointLLMResponseReceived, Name: "assembly-observer", Match: hook.Always(),
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
			called++
			active := services.Requests.ListBySession(row.ID)
			if len(active) != 1 || active[0].Kind != request.KindHook || active[0].Label != "assembly-observer" {
				t.Fatalf("missing hook request tracking: %#v", active)
			}
			return event, nil
		}),
	}); err != nil {
		t.Fatal(err)
	}
	event := hook.Event{Point: hook.PointLLMResponseReceived, Session: hook.SessionContext{ID: row.ID}, Message: hook.MessagePayload{Segments: llm.TextSegments("ordinary group message")}}
	groupCtx := platform.WithMessageContext(ctx, platform.MessageContext{Info: chatinfo.Info{Source: chatinfo.Source{ConversationKind: chatinfo.ConversationGroup}}})
	if _, err := hooks.Run(groupCtx, event); err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatal("unwoken group message ran the hook")
	}
	if _, err := hooks.Run(ctx, event); err != nil {
		t.Fatal(err)
	}
	if called != 1 || len(services.Requests.List()) != 0 {
		t.Fatalf("hook execution or cleanup failed: called=%d active=%v", called, services.Requests.List())
	}
}
