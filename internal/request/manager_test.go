package request

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/signal"
	"elbot/internal/testutil"
	"errors"
	"testing"
	"time"
)

func TestAllocationFailureDoesNotRegisterRequest(t *testing.T) {
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		records = append(records, record)

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	want := errors.New("reserve failed")
	m := NewManager(func(context.Context) (int64, error) { return 0, want })
	parent := t.Context()
	req, ctx, done, err := m.Start(parent, StartRequest{SessionID: "s1", Kind: KindTurn})
	if !errors.Is(err, want) || req.ID != "" || ctx != nil || done != nil || len(m.List()) != 0 {
		t.Fatalf("allocation failure left request state: %+v ctx=%v err=%v", req, ctx, err)
	}
	if parent.Err() != nil {
		t.Fatal("allocation canceled parent")
	}
	if len(records) != 0 {
		t.Fatalf("request reported a propagated error: %+v", records)
	}
}

func TestManagerStartListAndDone(t *testing.T) {
	m := NewManager(testutil.IDSequence())
	req, ctx, done, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindTurn, Label: "chat", Timeout: time.Minute})

	requests := m.List()
	if len(requests) != 1 || requests[0].ID != req.ID {
		t.Fatalf("List = %#v, want request %s", requests, req.ID)
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("expected deadline from request timeout")
	}

	done()
	done()
	if got := len(m.List()); got != 0 {
		t.Fatalf("active requests = %d, want 0", got)
	}
}

func TestManagerCancel(t *testing.T) {
	m := NewManager(testutil.IDSequence())
	req, ctx, done, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindTurn})
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
	m := NewManager(testutil.IDSequence())
	parent, ctx1, done1, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindTurn})
	defer done1()
	child, ctx2, done2, _ := m.Start(ctx1, StartRequest{ParentID: parent.ID, SessionID: "s1", Kind: KindTool})
	defer done2()
	_, ctx3, done3, _ := m.Start(context.Background(), StartRequest{SessionID: "s2", Kind: KindTurn})
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

func TestManagerTimeoutWaitsForExecutionCompletion(t *testing.T) {
	m := NewManager(testutil.IDSequence())
	req, ctx, done, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindTurn, Timeout: 10 * time.Millisecond})
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
	m := NewManager(testutil.IDSequence())
	parent, stopParent := context.WithCancel(t.Context())
	inherited, inheritedCtx, doneInherited, _ := m.Start(parent, StartRequest{Kind: KindHook})
	defer doneInherited()
	empty, emptyCtx, doneEmpty, _ := m.Start(t.Context(), StartRequest{Kind: KindHook})
	defer doneEmpty()
	_, otherCtx, doneOther, _ := m.Start(t.Context(), StartRequest{SessionID: "other", Kind: KindHook})
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
	if got := m.CancelSession("other"); got != 1 {
		t.Fatalf("CancelSession(other) = %d, want 1", got)
	}
	assertCanceled(t, otherCtx)
	doneInherited()
	doneEmpty()
	doneOther()
	if len(m.List()) != 0 {
		t.Fatal("completed requests remained registered")
	}
}
