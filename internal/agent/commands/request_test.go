package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/request"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
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
	turnReq, _, turnDone, err := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	defer turnDone()
	_, _, toolDone, err := manager.Start(ctx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})
	if err != nil {
		t.Fatalf("start tool: %v", err)
	}
	defer toolDone()

	cmd := NewRequests(Deps{Requests: manager, Store: store, Turns: turn.NewManager()})
	result, err := cmd.Handle(ctx, command.Request{})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	for _, want := range []string{"[1] chat turn request", "└── [1.1] shell tool request", "session: 测试会话", "tool: shell"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("requests output missing %q:\n%s", want, result.Content)
		}
	}
}

func newCommandTestStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "elbot_commands.db"))
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
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
	turnReq, _, turnDone, err := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	defer turnDone()
	_, _, toolDone, err := manager.Start(ctx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})
	if err != nil {
		t.Fatalf("start tool: %v", err)
	}
	defer toolDone()

	got := formatActiveRequests(ctx, Deps{Requests: manager, Store: store, Turns: turn.NewManager()}, manager.ListBySession("s1"))
	for _, want := range []string{"[1] chat turn request", "└── [1.1] shell tool request"} {
		if !strings.Contains(got, want) {
			t.Fatalf("active requests output missing %q:\n%s", want, got)
		}
	}
}

func TestStopCommandCancelsNumberedChildRequest(t *testing.T) {
	ctx := context.Background()
	manager := request.NewManager(time.Minute)
	turnReq, turnCtx, turnDone, err := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	defer turnDone()
	_, toolCtx, _, err := manager.Start(turnCtx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})
	if err != nil {
		t.Fatalf("start tool: %v", err)
	}

	cmd := NewStop(Deps{Requests: manager, Turns: turn.NewManager()})
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
	ctx := context.Background()
	manager := request.NewManager(time.Minute)
	turnReq, turnCtx, _, err := manager.Start(ctx, request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	_, toolCtx, _, err := manager.Start(turnCtx, request.StartRequest{ParentID: turnReq.ID, SessionID: "s1", Kind: request.KindTool, Label: "shell"})
	if err != nil {
		t.Fatalf("start tool: %v", err)
	}

	cmd := NewStop(Deps{Requests: manager, Turns: turn.NewManager()})
	if _, err := cmd.Handle(ctx, command.Request{Args: "1"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertCommandTestCanceled(t, turnCtx)
	assertCommandTestCanceled(t, toolCtx)
	if got := len(manager.List()); got != 0 {
		t.Fatalf("active requests = %d, want 0", got)
	}
}

func TestStopCommandCompletesRequestIDs(t *testing.T) {
	manager := request.NewManager(0)
	started, _, done, err := manager.Start(context.Background(), request.StartRequest{SessionID: "s1", Kind: request.KindLLM, Label: "chat"})
	if err != nil {
		t.Fatalf("start request: %v", err)
	}
	defer done()

	cmd := NewStop(Deps{Requests: manager}).(command.Completer)
	prefix := started.ID[:8]
	got := cmd.Complete(context.Background(), command.CompletionRequest{Raw: "/stop " + prefix, Prefix: "/", Name: "stop", Args: prefix, Cursor: len("/stop ") + len(prefix)})
	if len(got) != 1 || got[0].Text != started.ID || got[0].Kind != "request_id" {
		t.Fatalf("Complete = %#v", got)
	}
}
