package app

import (
	"context"
	"testing"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/llm"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
)

type slowStatusPlatform struct {
	assemblyPlatform
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
	d := newStatusDisplay(dispatch.New(dispatch.Options{Primary: p}))
	t.Cleanup(func() { _ = d.Close(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emit := func(version uint64, phase runtimestatus.Phase, tokens int) {
		t.Helper()
		usage := &llm.Usage{TotalTokens: tokens}
		if err := d.receive(ctx, agentevents.StatusChangedEvent{Snapshot: runtimestatus.Snapshot{SessionID: "s", Phase: phase, Usage: usage}, Version: version, Display: true}); err != nil {
			t.Fatal(err)
		}
		usage.TotalTokens = -1
	}
	emit(1, runtimestatus.PhaseLLM, 1)
	if got := nextStatus(t, p.sent); got.Phase != runtimestatus.PhaseLLM || got.Usage.TotalTokens != 1 {
		t.Fatal(got)
	}
	for i := uint64(2); i < 20; i++ {
		emit(i, runtimestatus.PhaseTool, int(i))
	}
	emit(20, runtimestatus.PhaseDone, 20)
	emit(19, runtimestatus.PhaseTool, 19) // Late lower version must not replace done.
	cancel()                              // Request cleanup must not cancel the final status projection.
	close(first)
	if got := nextStatus(t, p.sent); got.Phase != runtimestatus.PhaseDone || got.Usage.TotalTokens != 20 {
		t.Fatalf("lost terminal/coalescing: %+v", got)
	}
	emit(21, runtimestatus.PhaseLLM, 21)
	emit(22, runtimestatus.PhaseError, 22)
	close(second)
	if got := nextStatus(t, p.sent); got.Phase != runtimestatus.PhaseError || got.Usage.TotalTokens != 22 {
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
	one, two := &slowStatusPlatform{sent: make(chan runtimestatus.Snapshot, 3)}, &slowStatusPlatform{sent: make(chan runtimestatus.Snapshot, 3)}
	d := newStatusDisplay(dispatch.New(dispatch.Options{Primary: one}))
	t.Cleanup(func() { _ = d.Close(context.Background()) })
	e := agentevents.StatusChangedEvent{Snapshot: runtimestatus.Snapshot{SessionID: "same", Phase: runtimestatus.PhaseDone}, Version: 1, Display: true}
	for _, p := range []*slowStatusPlatform{one, two} {
		ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Sender: p})
		if err := d.receive(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	nextStatus(t, one.sent)
	nextStatus(t, two.sent)
	e.Display = false
	e.Version++
	if err := d.receive(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(one.sent) != 0 || len(two.sent) != 0 {
		t.Fatal("background status escaped")
	}
}
