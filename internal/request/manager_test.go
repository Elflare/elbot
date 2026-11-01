package request

import (
	"context"
	"elbot/internal/testutil"
	"errors"
	"testing"
	"time"
)

func TestAllocationFailureDoesNotRegisterRequest(t *testing.T) {
	want := errors.New("reserve failed")
	m := NewManager(time.Minute, func(context.Context) (int64, error) { return 0, want })
	parent := t.Context()
	req, ctx, done, err := m.Start(parent, StartRequest{SessionID: "s1", Kind: KindLLM})
	if !errors.Is(err, want) || req.ID != "" || ctx != nil || done != nil || len(m.List()) != 0 {
		t.Fatalf("allocation failure left request state: %+v ctx=%v err=%v", req, ctx, err)
	}
	if parent.Err() != nil {
		t.Fatal("allocation canceled parent")
	}
}

func TestManagerStartListAndDone(t *testing.T) {
	m := NewManager(time.Minute, testutil.IDSequence())
	req, _, done, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM, Label: "chat"})

	requests := m.List()
	if len(requests) != 1 || requests[0].ID != req.ID {
		t.Fatalf("List = %#v, want request %s", requests, req.ID)
	}
	if requests[0].Deadline == nil {
		t.Fatal("expected deadline from default timeout")
	}

	done()
	done()
	if got := len(m.List()); got != 0 {
		t.Fatalf("active requests = %d, want 0", got)
	}
}

func TestManagerCancel(t *testing.T) {
	m := NewManager(time.Minute, testutil.IDSequence())
	req, ctx, done, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM})
	defer done()
	got, ok := m.Get(req.ID)
	if !ok || got.ID != req.ID {
		t.Fatalf("Get = %#v, %v; want request %s", got, ok, req.ID)
	}

	if !m.Cancel(req.ID) {
		t.Fatal("Cancel returned false")
	}
	assertCanceled(t, ctx)
	if got, ok := m.Get(req.ID); !ok || !got.Stopping {
		t.Fatalf("canceled request must remain stopping until done: %+v, %v", got, ok)
	}
	if m.Cancel(req.ID) {
		t.Fatal("Cancel counted an already stopping request")
	}
	done()
	if _, ok := m.Get(req.ID); ok {
		t.Fatal("completed request remained registered")
	}
}

func TestManagerCancelSession(t *testing.T) {
	m := NewManager(time.Minute, testutil.IDSequence())
	parent, ctx1, done1, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindTurn})
	defer done1()
	child, ctx2, done2, _ := m.Start(ctx1, StartRequest{ParentID: parent.ID, SessionID: "s1", Kind: KindTool})
	defer done2()
	_, ctx3, done3, _ := m.Start(context.Background(), StartRequest{SessionID: "s2", Kind: KindLLM})
	defer done3()
	if child.ParentID != parent.ID {
		t.Fatalf("child parent = %q, want %q", child.ParentID, parent.ID)
	}

	if got := m.CancelSession("s1"); got != 2 {
		t.Fatalf("CancelSession = %d, want 2", got)
	}
	assertCanceled(t, ctx1)
	assertCanceled(t, ctx2)
	select {
	case <-ctx3.Done():
		t.Fatal("s2 request was canceled")
	default:
	}
	if got := len(m.ListBySession("s2")); got != 1 {
		t.Fatalf("s2 active requests = %d, want 1", got)
	}
}

func TestManagerCancelAll(t *testing.T) {
	m := NewManager(time.Minute, testutil.IDSequence())
	_, ctx1, done1, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM})
	defer done1()
	_, ctx2, done2, _ := m.Start(context.Background(), StartRequest{SessionID: "s2", Kind: KindTool})
	defer done2()

	if got := m.CancelAll(); got != 2 {
		t.Fatalf("CancelAll = %d, want 2", got)
	}
	assertCanceled(t, ctx1)
	assertCanceled(t, ctx2)
	if got := len(m.List()); got != 2 {
		t.Fatalf("stopping requests = %d, want 2", got)
	}
	if got := m.CancelAll(); got != 0 {
		t.Fatalf("repeated CancelAll = %d, want 0", got)
	}
}

func TestManagerTimeoutWaitsForExecutionCompletion(t *testing.T) {
	m := NewManager(10*time.Millisecond, testutil.IDSequence())
	req, ctx, done, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM})
	defer done()
	assertCanceled(t, ctx)
	if got, ok := m.Get(req.ID); !ok || !got.Stopping {
		t.Fatalf("timed out request disappeared before execution completed: %+v, %v", got, ok)
	}
	done()
	if len(m.List()) != 0 {
		t.Fatal("completed request remained registered")
	}
}

func assertCanceled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context was not canceled")
	}
}

func TestBatchCancelKeepsInheritedStopsAndMatchesEmptySession(t *testing.T) {
	m := NewManager(0, testutil.IDSequence())
	parent, stopParent := context.WithCancel(t.Context())
	inherited, inheritedCtx, doneInherited, _ := m.Start(parent, StartRequest{})
	defer doneInherited()
	empty, emptyCtx, doneEmpty, _ := m.Start(t.Context(), StartRequest{})
	defer doneEmpty()
	_, otherCtx, doneOther, _ := m.Start(t.Context(), StartRequest{SessionID: "other"})
	defer doneOther()
	stopParent()
	assertCanceled(t, inheritedCtx)
	if got, ok := m.Get(inherited.ID); !ok || !got.Stopping {
		t.Fatalf("inherited cancellation lost request: %+v, %v", got, ok)
	}
	if got := m.CancelSession(""); got != 1 {
		t.Fatalf("CancelSession(empty) = %d, want 1", got)
	}
	assertCanceled(t, emptyCtx)
	if otherCtx.Err() != nil || m.CancelSession("") != 0 || len(m.List()) != 3 {
		t.Fatal("session cancellation changed scope, count or registration")
	}
	if got, ok := m.Get(empty.ID); !ok || !got.Stopping {
		t.Fatalf("canceled request lost: %+v, %v", got, ok)
	}
	if got := m.CancelAll(); got != 1 {
		t.Fatalf("CancelAll = %d, want only remaining request", got)
	}
	assertCanceled(t, otherCtx)
	doneInherited()
	doneEmpty()
	doneOther()
	if len(m.List()) != 0 {
		t.Fatal("completed requests remained registered")
	}
}
