package turn

import (
	"context"
	"testing"
	"time"

	"elbot/internal/events"
)

func TestRiskConfirmationLateCleanupKeepsNextWait(t *testing.T) {
	m := NewManager()
	m.StartLLM("s", "hello")
	m.StartToolPhase("s")
	first := m.BeginRiskConfirmation("s", RiskConfirmation{ID: "first"})
	if first == nil || !m.ResolveRiskConfirmation("s", RiskConfirmationResponse{Confirmed: true, Extra: "first response"}) {
		t.Fatal("first confirmation was not registered")
	}
	resp, ok, status := first.Wait(context.Background(), 0)
	if !ok || !resp.Confirmed || resp.Extra != "first response" || status != events.ResultSucceeded {
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
	if resp, ok, status := second.Wait(context.Background(), 0); !ok || !resp.Rejected || status != events.ResultRejected {
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
	resp, ok, status := wait.Wait(ctx, 0)
	if ok || !resp.Stopped || resp.Expired || m.Snapshot("s").Phase != PhaseIdle || status != events.ResultCanceled {
		t.Fatalf("canceled wait=%+v ok=%v phase=%s", resp, ok, m.Snapshot("s").Phase)
	}
}

func TestRiskWaitDeclaresExpiryDeadlineAndStaleResults(t *testing.T) {
	for _, kind := range []string{"nil", "expired", "deadline", "stale"} {
		t.Run(kind, func(t *testing.T) {
			m := NewManager()
			m.StartLLM("s", "hello")
			m.StartToolPhase("s")
			wait := m.BeginRiskConfirmation("s", RiskConfirmation{ID: "first"})
			ctx := context.Background()
			var timeout time.Duration
			want := events.ResultFailed
			switch kind {
			case "nil":
				wait.Cancel()
				wait = nil
				want = events.ResultSkipped
			case "expired":
				timeout = time.Millisecond
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			case "stale":
				m.ResolveRiskConfirmation("s", RiskConfirmationResponse{Confirmed: true})
				m.StopSession("s")
				m.StartLLM("s", "next")
				m.StartToolPhase("s")
				second := m.BeginRiskConfirmation("s", RiskConfirmation{ID: "second"})
				defer second.Cancel()
				want = events.ResultSkipped
			}
			resp, ok, status := wait.Wait(ctx, timeout)
			if ok || status != want || resp.Expired != (kind == "expired") {
				t.Fatalf("response=%+v accepted=%v status=%s", resp, ok, status)
			}
			if kind == "stale" {
				if pending, ok := m.PendingRiskConfirmation("s"); !ok || pending.ID != "second" {
					t.Fatalf("replacement wait changed: %+v present=%v", pending, ok)
				}
			}
		})
	}
}
