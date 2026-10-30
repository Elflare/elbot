package builtin

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func TestRequestsCommandFormatsTree(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)
	sessionRow := &storage.Session{ID: "s1", Title: "测试会话", Mode: storage.SessionModeWork, OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "local", CreatedAt: storage.Now(), UpdatedAt: storage.Now()}
	if err := store.Sessions().Create(ctx, sessionRow); err != nil {
		t.Fatalf("create session: %v", err)
	}
	manager := request.NewManager(time.Minute)
	turnReq, _, turnDone := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer turnDone()
	_, _, toolDone := manager.Start(ctx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})
	defer toolDone()
	_, _, hookDone := manager.Start(ctx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindHook, Label: "gpt_image_draw_fullwidth"})
	defer hookDone()

	cmd := NewRequests(Deps{Requests: manager, Store: store, Turns: turn.NewSupervisor()})
	result, err := cmd.Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	for _, want := range []string{"[1] chat · turn", "[1.1]", "[1.2]", "shell · tool", "gpt_image_draw_fullwidth · hook", "session: 测试会话", "tool: shell", "hook: gpt_image_draw_fullwidth"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("requests output missing %q:\n%s", want, result.Content)
		}
	}
}

func assertCommandTestCanceled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context was not canceled")
	}
}

func TestFormatActiveRequestsUsesTree(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)
	sessionRow := &storage.Session{ID: "s1", Title: "状态会话", Mode: storage.SessionModeWork, OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "local", CreatedAt: storage.Now(), UpdatedAt: storage.Now()}
	if err := store.Sessions().Create(ctx, sessionRow); err != nil {
		t.Fatalf("create session: %v", err)
	}
	manager := request.NewManager(time.Minute)
	turnReq, _, turnDone := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer turnDone()
	_, _, toolDone := manager.Start(ctx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})
	defer toolDone()
	_, _, hookDone := manager.Start(ctx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindHook, Label: "gpt_image_draw_fullwidth"})
	defer hookDone()

	got := formatActiveRequests(ctx, Deps{Requests: manager, Store: store, Turns: turn.NewSupervisor()}, manager.ListBySession("s1"))
	for _, want := range []string{"[1] chat · turn", "[1.1]", "[1.2]", "shell · tool", "gpt_image_draw_fullwidth · hook", "mode: work", "tool: shell", "hook: gpt_image_draw_fullwidth"} {
		if !strings.Contains(got, want) {
			t.Fatalf("active requests output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "session:") || strings.Contains(got, sessionRow.Title) {
		t.Fatalf("active requests repeats session information:\n%s", got)
	}
}

func TestRequestCommandsWithoutActiveRequests(t *testing.T) {
	ctx := context.Background()
	deps := Deps{Requests: request.NewManager(0)}
	result, err := NewRequests(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "nil" {
		t.Fatalf("requests = %q", result.Content)
	}
	if got := formatActiveRequests(ctx, deps, nil); got != "requests=nil" {
		t.Fatalf("active requests = %q", got)
	}
}

func TestRequestFormatsPreserveDetails(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)
	row := &storage.Session{ID: "s1", Title: "运行会话", Mode: storage.SessionModeWork, CreatedAt: storage.Now(), UpdatedAt: storage.Now()}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	turns := turn.NewSupervisor()
	if !turns.StartAttempt(row.ID, turn.Input{}, turn.NewExecution("run"), "attempt") {
		t.Fatal("failed to start attempt")
	}
	t.Cleanup(func() {
		turns.StopSession(row.ID)
		turns.FinishAttempt(row.ID, "attempt")
	})
	turns.StartToolPhase(row.ID, "attempt")
	turns.AddToolUse(row.ID, "shell", "attempt")
	turns.AddToolUse(row.ID, "shell", "attempt")
	manager := request.NewManager(0)
	_, _, done := manager.Start(ctx, request.StartRequest{SessionID: row.ID, Kind: request.KindTurn, Label: "chat"})
	defer done()
	deps := Deps{Store: store, Turns: turns, RuntimeStatus: func(sessionID string) runtimestatus.Snapshot {
		if sessionID != row.ID {
			t.Fatalf("runtime session = %q, want %q", sessionID, row.ID)
		}
		return runtimestatus.Snapshot{Phase: runtimestatus.PhaseTool, ToolName: "shell", StageStartedAt: time.Now()}
	}}
	for _, tc := range []struct {
		name   string
		format func(context.Context, Deps, []request.Request) string
	}{
		{name: "requests", format: formatRequests},
		{name: "status", format: formatActiveRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.format(ctx, deps, manager.List())
			for _, want := range []string{"phase: tool shell (", "mode: work", "tools: shell×2"} {
				if !strings.Contains(got, want) {
					t.Fatalf("output missing %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestStopCommandCancelsNumberedHookRequest(t *testing.T) {
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	manager := request.NewManager(time.Minute)
	turnReq, turnCtx, turnDone := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer turnDone()
	_, hookCtx, _ := manager.Start(turnCtx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindHook, Label: "gpt_image_draw_fullwidth"})

	cmd := NewStop(Deps{Sessions: newSessionSupervisor(t, newCommandTestStore(t)), Requests: manager, Turns: turn.NewSupervisor()})
	if _, err := cmd.Handle(ctx, command.Request{Args: "1.1"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertCommandTestCanceled(t, hookCtx)
	select {
	case <-turnCtx.Done():
		t.Fatal("turn request was canceled by hook stop")
	default:
	}
}

func TestStopCommandCancelsNumberedChildRequest(t *testing.T) {
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	manager := request.NewManager(time.Minute)
	turnReq, turnCtx, turnDone := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer turnDone()
	_, toolCtx, _ := manager.Start(turnCtx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})

	cmd := NewStop(Deps{Sessions: newSessionSupervisor(t, newCommandTestStore(t)), Requests: manager, Turns: turn.NewSupervisor()})
	if _, err := cmd.Handle(ctx, command.Request{Args: "1.1"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertCommandTestCanceled(t, toolCtx)
	select {
	case <-turnCtx.Done():
		t.Fatal("turn request was canceled by child stop")
	default:
	}
}

func TestStopCommandCancelsNumberedTurnAndChildren(t *testing.T) {
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	manager := request.NewManager(time.Minute)
	turnReq, turnCtx, turnDone := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer turnDone()
	_, toolCtx, toolDone := manager.Start(turnCtx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})
	defer toolDone()

	cmd := NewStop(Deps{Sessions: newSessionSupervisor(t, newCommandTestStore(t)), Requests: manager, Turns: turn.NewSupervisor()})
	if _, err := cmd.Handle(ctx, command.Request{Args: "1"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertCommandTestCanceled(t, turnCtx)
	assertCommandTestCanceled(t, toolCtx)
	if got := len(manager.List()); got != 2 {
		t.Fatalf("stopping requests = %d, want 2", got)
	}
	toolDone()
	turnDone()
	if got := len(manager.List()); got != 0 {
		t.Fatalf("active requests = %d, want 0", got)
	}
}

func TestStopCommandCompletesRequestIDs(t *testing.T) {
	manager := request.NewManager(0)
	started, _, done := manager.Start(context.Background(), request.StartRequest{SessionID: "s1", Kind: request.KindLLM, Label: "chat"})
	defer done()

	cmd := NewStop(Deps{Sessions: newSessionSupervisor(t, newCommandTestStore(t)), Requests: manager}).(command.Completer)
	prefix := started.ID[:8]
	got := cmd.Complete(contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin}), command.CompletionRequest{Raw: "/stop " + prefix, Prefix: "/", Name: "stop", Args: prefix, Cursor: len("/stop ") + len(prefix)})
	if len(got) != 1 || got[0].Text != started.ID || got[0].Kind != "request_id" {
		t.Fatalf("Complete = %#v", got)
	}
}
