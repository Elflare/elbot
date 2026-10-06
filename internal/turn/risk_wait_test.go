package turn

import (
	"context"
	"testing"
)

func TestRiskConfirmationLateCleanupKeepsNextWait(t *testing.T) {
	m := NewManager()
	m.StartLLM("s", "hello")
	m.StartToolPhase("s")
	first := m.BeginRiskConfirmation("s", RiskConfirmation{ID: "first"})
	if first == nil || !m.ResolveRiskConfirmation("s", RiskConfirmationResponse{Confirmed: true, Extra: "first response"}) {
		t.Fatal("first confirmation was not registered")
	}
	resp, ok := first.Wait(context.Background(), 0)
	if !ok || !resp.Confirmed || resp.Extra != "first response" {
		t.Fatalf("early response=%+v ok=%v", resp, ok)
	}
	second := m.BeginRiskConfirmation("s", RiskConfirmation{ID: "second"})
	defer second.Cancel()
	if second == nil || first.Cancel() {
		t.Fatal("late cleanup removed the next confirmation on the same Turn")
	}
	if pending, ok := m.PendingRiskConfirmation("s"); !ok || pending.ID != "second" {
		t.Fatalf("next wait missing: %+v ok=%v", pending, ok)
	}
	if !m.ResolveRiskConfirmation("s", RiskConfirmationResponse{Rejected: true}) {
		t.Fatal("next response not accepted")
	}
	if resp, ok := second.Wait(context.Background(), 0); !ok || !resp.Rejected {
		t.Fatalf("next response=%+v ok=%v", resp, ok)
	}
}

func TestRiskConfirmationCancellationBeforeWait(t *testing.T) {
	m := NewManager()
	m.StartLLM("s", "hello")
	m.StartToolPhase("s")
	wait := m.BeginRiskConfirmation("s", RiskConfirmation{ID: "call"})
	if wait == nil {
		t.Fatal("confirmation was not registered")
	}
	// Even a queued approval cannot revive an already canceled request.
	m.ResolveRiskConfirmation("s", RiskConfirmationResponse{Confirmed: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, ok := wait.Wait(ctx, 0)
	if ok || !resp.Stopped || resp.Expired || m.Snapshot("s").Phase != PhaseIdle {
		t.Fatalf("canceled wait=%+v ok=%v phase=%s", resp, ok, m.Snapshot("s").Phase)
	}
}
