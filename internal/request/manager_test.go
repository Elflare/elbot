package request

import (
	"context"
	"testing"
	"time"
)

func TestManagerStartListAndDone(t *testing.T) {
	m := NewManager(time.Minute)
	req, _, done, err := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM, Label: "chat"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

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
	req, ctx, _, err := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !m.Cancel(req.ID) {
		t.Fatal("Cancel returned false")
	}
	assertCanceled(t, ctx)
	if got := len(m.List()); got != 0 {
		t.Fatalf("active requests = %d, want 0", got)
	}
	if m.Cancel(req.ID) {
		t.Fatal("Cancel returned true for missing request")
	}
}

func TestManagerCancelSession(t *testing.T) {
	m := NewManager(time.Minute)
	_, ctx1, _, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM})
	_, ctx2, _, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindTool})
	_, ctx3, _, _ := m.Start(context.Background(), StartRequest{SessionID: "s2", Kind: KindLLM})

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
	_, ctx1, _, _ := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM})
	_, ctx2, _, _ := m.Start(context.Background(), StartRequest{SessionID: "s2", Kind: KindTool})

	if got := m.CancelAll(); got != 2 {
		t.Fatalf("CancelAll = %d, want 2", got)
	}
	assertCanceled(t, ctx1)
	assertCanceled(t, ctx2)
	if got := len(m.List()); got != 0 {
		t.Fatalf("active requests = %d, want 0", got)
	}
}

func TestManagerTimeoutCleansRequest(t *testing.T) {
	m := NewManager(10 * time.Millisecond)
	_, ctx, _, err := m.Start(context.Background(), StartRequest{SessionID: "s1", Kind: KindLLM})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	assertCanceled(t, ctx)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(m.List()) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for request cleanup")
}

func assertCanceled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context was not canceled")
	}
}
