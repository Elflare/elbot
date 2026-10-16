package request

import (
	"context"
	"testing"
	"time"
)

func TestManagerStartListAndDone(t *testing.T) {
	m := NewManager(time.Minute)
	req, _, done := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM, Label: "chat"})

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
	m := NewManager(time.Minute)
	req, ctx, done := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM})
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
	m := NewManager(time.Minute)
	parent, ctx1, done1 := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindTurn})
	defer done1()
	child, ctx2, done2 := m.Start(ctx1, StartRequest{ParentID: parent.ID, SessionID: "s1", Kind: KindTool})
	defer done2()
	_, ctx3, done3 := m.Start(context.Background(), StartRequest{SessionID: "s2", Kind: KindLLM})
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
	m := NewManager(time.Minute)
	_, ctx1, done1 := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM})
	defer done1()
	_, ctx2, done2 := m.Start(context.Background(), StartRequest{SessionID: "s2", Kind: KindTool})
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
	m := NewManager(10 * time.Millisecond)
	req, ctx, done := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM})
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
