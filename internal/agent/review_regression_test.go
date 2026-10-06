package agent

import (
	"context"
	"elbot/internal/chatinfo"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/platform"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"testing"
)

func TestReviewCompactRejectsDirectiveBeforeStateMutation(t *testing.T) {
	ctx := context.Background()
	p := &fakePlatform{}

	registry := tool.NewRegistry()
	if err := registry.Register(builtin.NewWebSearchTool()); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, p, &fakeLLM{}, "m", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
		cfg.ToolRegistry = registry
	})
	row, err := a.execution.sessions.Create(ctx, a.identity.Scope(ctx), session.CreateRequest{Title: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.execution.turns.StartCompactRun(row.ID, "compact") {
		t.Fatal("cannot start compact")
	}
	defer a.execution.turns.CompleteCompactRun(row.ID, "compact")
	if err := a.HandleMessage(ctx, "@tool:web_search"); err != nil {
		t.Fatal(err)
	}
	state, err := a.execution.dialogue.Preparer.Tools.State.Snapshot(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ToolCache) != 0 {
		t.Fatalf("rejected input persisted %d tools; output=%q", len(state.ToolCache), p.out.String())
	}
}

func TestStopCompletionUsesResolvedActorAndCurrentSession(t *testing.T) {
	ctx := context.Background()

	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "m", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
		cfg.SecurityPolicy = security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}})
	})
	user := platform.WithMessageContext(ctx, platform.MessageContext{Info: chatinfo.Info{Source: chatinfo.Source{Platform: "cli", ScopeID: "user"}, Identity: chatinfo.Identity{PlatformUserID: "user"}}})
	row, err := a.execution.sessions.Create(user, a.identity.Scope(user), session.CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, done, err := a.execution.requests.Start(ctx, request.StartRequest{SessionID: "foreign", Kind: request.KindTurn})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	current, _, done, err := a.execution.requests.Start(ctx, request.StartRequest{SessionID: row.ID, Kind: request.KindTurn})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	query := completion.Request{Text: "/stop "}
	adminItems := a.CompletionService().Complete(ctx, query)
	if len(adminItems) != 2 {
		t.Fatalf("admin completion lost global access: %+v", adminItems)
	}
	userItems := a.CompletionService().Complete(user, query)
	if len(userItems) != 1 || userItems[0].Text != current.ID || userItems[0].Text == foreign.ID {
		t.Fatalf("user completion=%+v", userItems)
	}
}

func TestReviewTurnHookReadonlySelection(t *testing.T) {
	for _, point := range []hook.Point{hook.PointLLMTurnPrepared, hook.PointLLMRequestPrepared} {
		t.Run(string(point), func(t *testing.T) { testReadonlyHookSelection(t, point) })
	}
}

func testReadonlyHookSelection(t *testing.T, point hook.Point) {
	old := &fakeLLM{replies: []string{"old"}}
	next := &fakeLLM{replies: []string{"next"}}
	models := newTestModels(t, modelmgr.Options{Clients: map[string]llm.LLM{"old": old, "next": next}, Providers: map[string]config.ProviderConfig{"old": {Models: []string{"first"}}, "next": {Models: []string{"second"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "old", Model: "first"}}})
	opts := validConstructorOptions(t)
	opts.Models = models
	opts.SessionConfig.NamingConfig.TriggerStep = 100

	h := hook.NewManager()
	if err := h.Register(hook.Registration{Point: point, Name: "review", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, e hook.Event) (hook.Event, error) {
		e.LLM.Provider = "next"
		e.LLM.Model = "second"
		e.Message.Segments = llm.TextSegments("hook message")
		return e, nil
	})}); err != nil {
		t.Fatal(err)
	}
	a := mustNewWithOptions(t, opts, func(cfg *testAgentOptions) {
		cfg.HookManager = h
	})
	if err := a.HandleMessage(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	got := old.chatRequests()
	if point == hook.PointLLMTurnPrepared && len(got) == 1 && llm.SegmentsTextOnly(got[0].Messages[len(got[0].Messages)-1].Segments) != "hook message" {
		t.Fatal("allowed message modification was lost")
	}
	if len(got) != 1 || got[0].Model != "first" || next.requestCount() != 0 {
		t.Fatalf("read-only selection changed: old client requests=%#v next count=%d", got, next.requestCount())
	}
}

func TestReviewStopCannotCancelOtherUsersRequest(t *testing.T) {
	ctx := context.Background()
	p := &fakePlatform{}

	a := newTestAgent(t, p, &fakeLLM{}, "m", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
		cfg.SecurityPolicy = security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}})
	})
	victim, err := a.execution.sessions.Create(ctx, session.Scope{ActorID: "cli:victim", Platform: "cli", PlatformScopeID: "victim"}, session.CreateRequest{Title: "victim"})
	if err != nil {
		t.Fatal(err)
	}
	_, victimCtx, done, err := a.execution.requests.Start(ctx, request.StartRequest{SessionID: victim.ID, Kind: request.KindTurn, Label: "victim"})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	attacker := platform.WithMessageContext(ctx, platform.MessageContext{Info: chatinfo.Info{Source: chatinfo.Source{Platform: "cli", ScopeID: "attacker"}, Identity: chatinfo.Identity{PlatformUserID: "attacker"}}})
	if err := a.HandleMessage(attacker, "/stop 1"); err != nil {
		t.Fatal(err)
	}
	if victimCtx.Err() != nil {
		t.Fatalf("other user's request canceled: %v; output=%q", victimCtx.Err(), p.out.String())
	}
}

func TestReviewOldInputCannotCommitToolsAfterBindingSwitch(t *testing.T) {
	ctx := context.Background()
	p := &fakePlatform{}

	registry := tool.NewRegistry()
	if err := registry.Register(builtin.NewWebSearchTool()); err != nil {
		t.Fatal(err)
	}
	started, release, unblock := modelBarrier(t)
	h := hook.NewManager()
	if err := h.Register(hook.Registration{Point: hook.PointAgentInputPrepared, Name: "review.block", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, e hook.Event) (hook.Event, error) { close(started); <-release; return e, nil })}); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, p, &fakeLLM{}, "m", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
		cfg.ToolRegistry = registry
		cfg.HookManager = h
	})
	row, err := a.execution.sessions.Create(ctx, a.identity.Scope(ctx), session.CreateRequest{Title: "old"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "@tool:web_search") }()
	awaitModelBarrier(t, started)
	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-done; err == nil {
		t.Fatal("stale input was accepted")
	}
	state, err := a.execution.dialogue.Preparer.Tools.State.Snapshot(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ToolCache) > 0 {
		t.Fatalf("expired input committed tools after /new; output=%q", p.out.String())
	}
}
