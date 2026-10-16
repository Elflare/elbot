package turn

import (
	"context"
	"testing"

	"elbot/internal/events"
)

func TestStopKeepsAttemptUntilAcknowledged(t *testing.T) {
	m := NewManager()
	e := NewExecution("old")
	m.StartExecution("s", Input{Text: "original"}, e, "old-attempt")
	done := m.Snapshot("s").AttemptDone
	m.StopSession("s", "old-attempt")
	if got := m.Snapshot("s"); got.Phase != PhaseStopping || got.AttemptDone != done {
		t.Fatalf("stopping snapshot: %+v", got)
	}
	m.StopSession("s")
	if repeated := m.Snapshot("s").AttemptDone; repeated != done {
		t.Fatal("repeated stop replaced the original notification")
	}
	if m.StartExecution("s", Input{}, NewExecution("new"), "new-attempt") {
		t.Fatal("new execution entered before old work exited")
	}
	select {
	case <-done:
		t.Fatal("attempt completed before acknowledgement")
	case <-e.Done():
		t.Fatal("logical execution completed before acknowledgement")
	default:
	}
	m.FinishRequest("s", "old-attempt")
	select {
	case <-done:
	default:
		t.Fatal("completion did not wake waiters")
	}
	if !m.StartExecution("s", Input{}, NewExecution("new"), "new-attempt") {
		t.Fatal("new execution could not enter after old work exited")
	}
	m.FinishRequest("s", "old-attempt")
	if !m.MatchesAttempt("s", "new-attempt") {
		t.Fatal("late old completion removed replacement")
	}
}

func TestAppendAttemptAcknowledgementKeepsConfirmation(t *testing.T) {
	m := NewManager()
	e := NewExecution("execution")
	m.StartExecution("s", Input{Text: "first"}, e, "attempt")
	done := m.Snapshot("s").AttemptDone
	m.InterruptLLM("s", "more")
	m.FinishRequest("s", "attempt")
	select {
	case <-done:
	default:
		t.Fatal("old attempt did not acknowledge exit")
	}
	if snapshot := m.Snapshot("s"); snapshot.Phase != PhaseAwaitAppendConfirm || snapshot.AttemptDone != nil {
		t.Fatalf("append confirmation lost: %+v", snapshot)
	}
	select {
	case <-e.Done():
		t.Fatal("attempt exit ended the paused logical execution")
	default:
	}
}

func TestStoppingAttemptCannotConsumeQueuedApproval(t *testing.T) {
	m := NewManager()
	m.StartExecution("s", Input{}, NewExecution("execution"), "attempt")
	m.StartToolPhase("s", "attempt")
	wait := m.BeginRiskConfirmation("s", RiskConfirmation{ID: "call"}, "attempt")
	m.ResolveRiskConfirmation("s", RiskConfirmationResponse{Confirmed: true})
	m.StopSession("s", "attempt")
	resp, allowed, status := wait.Wait(context.Background(), 0)
	if allowed || !resp.Stopped || status != events.ResultCanceled || m.Snapshot("s").Phase != PhaseStopping {
		t.Fatalf("stopping attempt revived: %+v allowed=%v status=%s", resp, allowed, status)
	}
	m.FinishRequest("s", "attempt")
}
