package cli

import (
	"context"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
)

type slowStatusPlatform struct {
	sent  chan runtimestatus.Snapshot
	gates []chan struct{}
	calls int
}

func (p *slowStatusPlatform) SetRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) error {
	p.sent <- snapshot
	index := p.calls
	p.calls++
	if index < len(p.gates) {
		select {
		case <-p.gates[index]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func nextStatus(t *testing.T, sent <-chan runtimestatus.Snapshot) runtimestatus.Snapshot {
	t.Helper()
	select {
	case s := <-sent:
		return s
	case <-time.After(time.Second):
		t.Fatal("status update was never scheduled")
		return runtimestatus.Snapshot{}
	}
}

func TestStatusCoalescesTerminalAndUpdatesDuringSend(t *testing.T) {
	first, second := make(chan struct{}), make(chan struct{})
	p := &slowStatusPlatform{sent: make(chan runtimestatus.Snapshot, 10), gates: []chan struct{}{first, second}}
	d := newStatusDisplay()
	d.send = func(ctx context.Context, _ messageData, snapshot runtimestatus.Snapshot) error {
		return p.SetRuntimeStatus(ctx, snapshot)
	}
	t.Cleanup(func() { _ = d.Close(context.Background()) })
	ctx, cancel := context.WithCancel(contextinfo.WithConversation(context.Background(), contextinfo.Conversation{PlatformData: messageData{adapter: New()}}))
	defer cancel()
	emit := func(version uint64, phase runtimestatus.Phase, tokens int) {
		t.Helper()
		usage := &llm.Usage{TotalTokens: func() *int64 { n := int64(tokens); return &n }()}
		if err := d.receive(StatusChangedEvent{Conversation: testConversation(ctx), Snapshot: runtimestatus.Snapshot{SessionID: "s", Phase: phase, Usage: usage}, Version: version}); err != nil {
			t.Fatal(err)
		}
	}
	emit(1, runtimestatus.PhaseLLM, 1)
	if got := nextStatus(t, p.sent); got.Phase != runtimestatus.PhaseLLM || (got.Usage.TotalTokens == nil || *got.Usage.TotalTokens != 1) {
		t.Fatal(got)
	}
	for i := uint64(2); i < 20; i++ {
		emit(i, runtimestatus.PhaseTool, int(i))
	}
	emit(20, runtimestatus.PhaseDone, 20)
	emit(19, runtimestatus.PhaseTool, 19) // Late lower version must not replace done.
	cancel()                              // Request cleanup must not cancel the final status projection.
	close(first)
	if got := nextStatus(t, p.sent); got.Phase != runtimestatus.PhaseDone || (got.Usage.TotalTokens == nil || *got.Usage.TotalTokens != 20) {
		t.Fatalf("lost terminal/coalescing: %+v", got)
	}
	emit(21, runtimestatus.PhaseLLM, 21)
	emit(22, runtimestatus.PhaseError, 22)
	close(second)
	if got := nextStatus(t, p.sent); got.Phase != runtimestatus.PhaseError || (got.Usage.TotalTokens == nil || *got.Usage.TotalTokens != 22) {
		t.Fatalf("lost update during send: %+v", got)
	}
	deadline := time.Now().Add(time.Second)
	for {
		d.mu.Lock()
		remaining := len(d.latest)
		d.mu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal projection retained")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStatusTargetsKeepDistinctSendersAndBackgroundIsSilent(t *testing.T) {
	one, two := New(), New()
	d := newStatusDisplay()
	sent := make(chan messageData, 2)
	d.send = func(_ context.Context, target messageData, _ runtimestatus.Snapshot) error {
		sent <- target
		return nil
	}
	t.Cleanup(func() { _ = d.Close(context.Background()) })
	e := StatusChangedEvent{Snapshot: runtimestatus.Snapshot{SessionID: "same", Phase: runtimestatus.PhaseDone}, Version: 1}
	for _, a := range []*Adapter{one, two} {
		ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{PlatformData: messageData{adapter: a}})
		if err := d.receive(StatusChangedEvent{Snapshot: e.Snapshot, Version: e.Version, Conversation: testConversation(ctx)}); err != nil {
			t.Fatal(err)
		}
	}
	targets := map[messageData]bool{}
	for range 2 {
		select {
		case target := <-sent:
			targets[target] = true
		case <-time.After(time.Second):
			t.Fatal("display missing")
		}
	}
	if len(targets) != 2 {
		t.Fatal("original targets merged")
	}
	e.Version++
	background := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli"}})
	if err := d.receive(StatusChangedEvent{Snapshot: e.Snapshot, Version: e.Version, Conversation: testConversation(background)}); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatal("background status escaped")
	}
}
