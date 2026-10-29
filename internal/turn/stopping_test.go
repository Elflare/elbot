package turn

import (
	"testing"
)

func TestStopKeepsAttemptUntilAcknowledged(t *testing.T) {
	m := NewSupervisor()
	e := NewExecution("old")
	m.StartAttempt("s", Input{Text: "original"}, e, "old-attempt")
	done := m.Snapshot("s").AttemptDone
	m.StopAttempt("s", "old-attempt")
	if got := m.Snapshot("s"); got.Phase != PhaseStopping || got.AttemptDone != done {
		t.Fatalf("stopping snapshot: %+v", got)
	}
	m.StopSession("s")
	if repeated := m.Snapshot("s").AttemptDone; repeated != done {
		t.Fatal("repeated stop replaced the original notification")
	}
	if m.StartAttempt("s", Input{}, NewExecution("new"), "new-attempt") {
		t.Fatal("new execution entered before old work exited")
	}
	select {
	case <-done:
		t.Fatal("attempt completed before acknowledgement")
	case <-e.Done():
		t.Fatal("logical execution completed before acknowledgement")
	default:
	}
	m.FinishAttempt("s", "old-attempt")
	select {
	case <-done:
	default:
		t.Fatal("completion did not wake waiters")
	}
	if !m.StartAttempt("s", Input{}, NewExecution("new"), "new-attempt") {
		t.Fatal("new execution could not enter after old work exited")
	}
	m.FinishAttempt("s", "old-attempt")
	if m.Snapshot("s").Attempt != "new-attempt" {
		t.Fatal("late old completion removed replacement")
	}
}

func TestAppendAttemptAcknowledgementKeepsConfirmation(t *testing.T) {
	m := NewSupervisor()
	e := NewExecution("execution")
	m.StartAttempt("s", Input{Text: "first"}, e, "attempt")
	done := m.Snapshot("s").AttemptDone
	m.BeginAppend("s", "attempt", Input{Text: "more"})
	m.FinishAttempt("s", "attempt")
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
