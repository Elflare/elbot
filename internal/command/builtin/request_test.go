package builtin

import (
	"context"
	"elbot/internal/testutil"
	"strings"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/request"
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
	manager := request.NewManager(time.Minute, testutil.IDSequence())
	turnReq, _, turnDone, _ := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer turnDone()
	_, _, toolDone, _ := manager.Start(ctx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})
	defer toolDone()
	_, _, hookDone, _ := manager.Start(ctx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindHook, Label: "gpt_image_draw_fullwidth"})
	defer hookDone()

	cmd := NewRequests(Deps{CommandText: testCommandText, Requests: manager, Store: store})
	result, err := cmd.Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	for _, want := range []string{"[1] turn: chat · ", "[1.1] tool: shell · ", "[1.2] hook: gpt_image_draw_fullwidth · ", "session: 测试会话"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("requests output missing %q:\n%s", want, result.Content)
		}
	}
	if strings.Count(result.Content, "shell") != 1 || strings.Count(result.Content, "gpt_image_draw_fullwidth") != 1 {
		t.Fatalf("requests repeats child names:\n%s", result.Content)
	}
	for _, unwanted := range []string{"phase:", "mode:", "tools:"} {
		if strings.Contains(result.Content, unwanted) {
			t.Fatalf("requests contains session details %q:\n%s", unwanted, result.Content)
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
	manager := request.NewManager(time.Minute, testutil.IDSequence())
	turnReq, _, turnDone, _ := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer turnDone()
	_, _, toolDone, _ := manager.Start(ctx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})
	defer toolDone()
	_, _, hookDone, _ := manager.Start(ctx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindHook, Label: "gpt_image_draw_fullwidth"})
	defer hookDone()

	got := formatActiveRequests(manager.ListBySession("s1"))
	for _, want := range []string{"[1] turn: chat · ", "[1.1] tool: shell · ", "[1.2] hook: gpt_image_draw_fullwidth · "} {
		if !strings.Contains(got, want) {
			t.Fatalf("active requests output missing %q:\n%s", want, got)
		}
	}
	if len(strings.Split(got, "\n")) != 4 {
		t.Fatalf("active requests contains extra details:\n%s", got)
	}
}

func TestRequestCommandsWithoutActiveRequests(t *testing.T) {
	ctx := context.Background()
	deps := Deps{CommandText: testCommandText, Requests: request.NewManager(0, testutil.IDSequence())}
	result, err := NewRequests(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "nil" {
		t.Fatalf("requests = %q", result.Content)
	}
	if got := formatActiveRequests(nil); got != "" {
		t.Fatalf("active requests = %q", got)
	}
}

func TestWriteRequestLineKindsAndStopping(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     request.Kind
		label    string
		stopping bool
		want     string
	}{
		{name: "turn", kind: request.KindTurn, label: "chat", want: "turn: chat"},
		{name: "tool", kind: request.KindTool, label: "draw", want: "tool: draw"},
		{name: "hook", kind: request.KindHook, label: "draw", want: "hook: draw"},
		{name: "compress", kind: request.KindCompress, label: "compact", want: "compress: compact"},
		{name: "stopping", kind: request.KindTool, label: "draw", stopping: true, want: "tool: draw stopping"},
		{name: "unnamed turn", kind: request.KindTurn, want: "turn: chat"},
		{name: "unnamed hook", kind: request.KindHook, want: "hook: request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, prefix := range []string{"", "└── "} {
				var sb strings.Builder
				writeRequestLine(&sb, "1", prefix, request.Request{
					Kind: tc.kind, Label: tc.label, Stopping: tc.stopping, StartedAt: time.Now(),
				})
				got := sb.String()
				if !strings.HasPrefix(got, "  "+prefix+"[1] "+tc.want+" · ") || strings.Count(got, "\n") != 1 {
					t.Fatalf("request line = %q", got)
				}
			}
		})
	}
}

func TestStopCommandCancelsNumberedHookRequest(t *testing.T) {
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	manager := request.NewManager(time.Minute, testutil.IDSequence())
	turnReq, turnCtx, turnDone, _ := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer turnDone()
	_, hookCtx, _, _ := manager.Start(turnCtx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindHook, Label: "gpt_image_draw_fullwidth"})

	cmd := NewStop(Deps{CommandText: testCommandText, Sessions: newSessionSupervisor(t, newCommandTestStore(t)), Requests: manager, Turns: turn.NewSupervisor(testutil.IDSequence())})
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
	manager := request.NewManager(time.Minute, testutil.IDSequence())
	turnReq, turnCtx, turnDone, _ := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer turnDone()
	_, toolCtx, _, _ := manager.Start(turnCtx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})

	cmd := NewStop(Deps{CommandText: testCommandText, Sessions: newSessionSupervisor(t, newCommandTestStore(t)), Requests: manager, Turns: turn.NewSupervisor(testutil.IDSequence())})
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
	manager := request.NewManager(time.Minute, testutil.IDSequence())
	turnReq, turnCtx, turnDone, _ := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer turnDone()
	_, toolCtx, toolDone, _ := manager.Start(turnCtx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})
	defer toolDone()

	cmd := NewStop(Deps{CommandText: testCommandText, Sessions: newSessionSupervisor(t, newCommandTestStore(t)), Requests: manager, Turns: turn.NewSupervisor(testutil.IDSequence())})
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
	manager := request.NewManager(0, testutil.IDSequence())
	started, _, done, _ := manager.Start(context.Background(), request.StartRequest{SessionID: "s1", Kind: request.KindLLM, Label: "chat"})
	defer done()

	cmd := NewStop(Deps{CommandText: testCommandText, Sessions: newSessionSupervisor(t, newCommandTestStore(t)), Requests: manager}).(command.Completer)
	prefix := started.ID[:len(started.ID)-1]
	got := cmd.Complete(contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin}), command.CompletionRequest{Raw: "/stop " + prefix, Prefix: "/", Name: "stop", Args: prefix, Cursor: len("/stop ") + len(prefix)})
	if len(got) != 1 || got[0].Text != started.ID || got[0].Kind != "request_id" {
		t.Fatalf("Complete = %#v", got)
	}
}
