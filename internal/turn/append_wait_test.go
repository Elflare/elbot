package turn

import (
	"context"
	"testing"
	"time"
)

func TestAppendWaitCancellationDoesNotRemoveReplacement(t *testing.T) {
	m := NewManager()
	e := NewExecution("execution")
	m.StartExecution("session", Input{Text: "first"}, e, "attempt")
	m.InterruptLLM("session", "more")
	old := m.AppendWait("session")
	// ResumeAppend reuses the state object; identity must include the wait itself.
	if _, _, ok := m.ResumeAppend("session"); !ok || !m.InterruptLLM("session", "replacement") {
		t.Fatal("failed to replace confirmation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if old.Wait(ctx, time.Nanosecond) {
		t.Fatal("old wait expired a replacement")
	}
	if got := m.Snapshot("session"); got.Phase != PhaseAwaitAppendConfirm || got.PendingCount != 1 {
		t.Fatalf("replacement lost: %+v", got)
	}
	if m.AppendWait("session").Wait(ctx, 0) || m.Snapshot("session").Phase != PhaseIdle {
		t.Fatal("application cancellation did not silently discard an unbounded wait")
	}
}
