package builtin

import (
	"context"
	"elbot/internal/testutil"
	"strings"
	"testing"
	"time"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/request"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func TestRequestsCommandFormatsTree(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)
	sessionRow := &storage.Session{ID: "s1", Title: "测试会话", Mode: storage.SessionModeWork, Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}, Platform: "cli", ChatID: "local", CreatedAt: storage.Now(), UpdatedAt: storage.Now(), ChatType: "private"}
	if err := store.Sessions().Create(ctx, sessionRow); err != nil {
		t.Fatalf("create session: %v", err)
	}
	manager := request.NewManager(testutil.IDSequence())
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
	data := result.Data.(command.RequestsData)
	if len(data.Items) != 3 || data.Items[0].Kind != request.KindTurn || data.Items[1].ParentID != data.Items[0].ID || data.Items[1].Label != "shell" || data.Items[2].ParentID != data.Items[0].ID || data.Items[2].SessionTitle != "测试会话" {
		t.Fatalf("requests data = %+v", data)
	}
}

func TestRequestDataOrderMatchesStopNumbering(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)
	if err := store.Sessions().Create(ctx, &storage.Session{ID: "s1", Title: "session", Mode: storage.SessionModeWork, Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}, Platform: "cli", ChatID: "local", CreatedAt: storage.Now(), UpdatedAt: storage.Now(), ChatType: "private"}); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []request.Request{
		{ID: "root-z", SessionID: "s1", Kind: request.KindTurn, StartedAt: started},
		{ID: "child-z", ParentID: "root-a", SessionID: "s1", Kind: request.KindTool, StartedAt: started},
		{ID: "root-a", SessionID: "s1", Kind: request.KindTurn, StartedAt: started},
		{ID: "child-a", ParentID: "root-a", SessionID: "s1", Kind: request.KindTool, StartedAt: started},
	}
	data := requestData(ctx, Deps{Store: store}, rows)
	if got := []string{data[0].ID, data[1].ID, data[2].ID, data[3].ID}; strings.Join(got, ",") != "child-a,child-z,root-a,root-z" {
		t.Fatalf("requestData order = %v", got)
	}
	if id, ok := resolveRequestArg(rows, "1"); !ok || id != "root-a" {
		t.Fatalf("stop number 1 resolved to %q, %v", id, ok)
	}
	if id, ok := resolveRequestArg(rows, "1.1"); !ok || id != "child-a" {
		t.Fatalf("stop number 1.1 resolved to %q, %v", id, ok)
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

func TestRequestCommandsWithoutActiveRequests(t *testing.T) {
	ctx := context.Background()
	deps := Deps{CommandText: testCommandText, Requests: request.NewManager(testutil.IDSequence())}
	result, err := NewRequests(deps).Handle(ctx, command.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Data.(command.RequestsData).Items; len(got) != 0 {
		t.Fatalf("requests items = %+v", got)
	}
}

func TestStopCommandCancelsNumberedHookRequest(t *testing.T) {
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	manager := request.NewManager(testutil.IDSequence())
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
	manager := request.NewManager(testutil.IDSequence())
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
	manager := request.NewManager(testutil.IDSequence())
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
	manager := request.NewManager(testutil.IDSequence())
	started, _, done, _ := manager.Start(context.Background(), request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer done()

	cmd := NewStop(Deps{CommandText: testCommandText, Sessions: newSessionSupervisor(t, newCommandTestStore(t)), Requests: manager}).(command.Completer)
	prefix := started.ID[:len(started.ID)-1]
	got := cmd.Complete(contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin}), command.CompletionRequest{Raw: "/stop " + prefix, Prefix: "/", Name: "stop", Args: prefix, Cursor: len("/stop ") + len(prefix)})
	if len(got) != 1 || got[0].Text != started.ID || got[0].Kind != "request_id" {
		t.Fatalf("Complete = %#v", got)
	}
}
