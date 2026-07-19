package turn

import (
	"testing"
	"time"
)

func TestConfirmAndCancelTokens(t *testing.T) {
	for _, token := range []string{"$", "是", "y", "yes", " YES "} {
		if !IsConfirm(token) {
			t.Fatalf("IsConfirm(%q) = false", token)
		}
	}
	for _, token := range []string{"取消", "否", "n", "no", " NO "} {
		if !IsCancel(token) {
			t.Fatalf("IsCancel(%q) = false", token)
		}
	}
}

func TestInterruptAndConfirmMergesOriginalInput(t *testing.T) {
	m := NewManager()
	if !m.StartLLM("s1", "zz") {
		t.Fatal("StartLLM returned false")
	}
	if !m.InterruptLLM("s1", "等等！") {
		t.Fatal("InterruptLLM returned false")
	}
	if !m.AppendPending("s1", "再补一句") {
		t.Fatal("AppendPending returned false")
	}

	snapshot := m.Snapshot("s1")
	if snapshot.Phase != PhaseAwaitAppendConfirm || snapshot.PendingCount != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	merged, ok := m.ConfirmAppend("s1")
	if !ok {
		t.Fatal("ConfirmAppend returned false")
	}
	want := "补充信息：\n1. zz\n2. 等等！\n3. 再补一句"
	if merged != want {
		t.Fatalf("merged = %q, want %q", merged, want)
	}
	if got := m.Snapshot("s1").Phase; got != PhaseIdle {
		t.Fatalf("phase = %s, want idle", got)
	}
}

func TestCancelAppendDropsTurn(t *testing.T) {
	m := NewManager()
	m.StartLLM("s1", "zz")
	m.InterruptLLM("s1", "等等！")

	if !m.CancelAppend("s1") {
		t.Fatal("CancelAppend returned false")
	}
	if got := m.Snapshot("s1").Phase; got != PhaseIdle {
		t.Fatalf("phase = %s, want idle", got)
	}
}

func TestToolPendingDrain(t *testing.T) {
	m := NewManager()
	m.AddToolUse("s1", "web_search")
	m.AddToolUse("s1", "web_search")
	if !m.AppendPending("s1", "补充 A") {
		t.Fatal("AppendPending returned false")
	}
	if !m.AppendPending("s1", "补充 B") {
		t.Fatal("AppendPending returned false")
	}

	snapshot := m.Snapshot("s1")
	if snapshot.Phase != PhaseTool || snapshot.PendingCount != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if got := ToolsString(snapshot.Tools); got != "web_search x2" {
		t.Fatalf("ToolsString = %q", got)
	}

	merged := m.DrainMerged("s1")
	want := "补充信息：\n1. 补充 A\n2. 补充 B"
	if merged != want {
		t.Fatalf("merged = %q, want %q", merged, want)
	}
	if got := m.Snapshot("s1").PendingCount; got != 0 {
		t.Fatalf("pending = %d, want 0", got)
	}
}

func TestCompleteLLMReturnsMergedPending(t *testing.T) {
	m := NewManager()
	if !m.StartLLM("s1", "先查") {
		t.Fatal("StartLLM returned false")
	}
	if !m.StartToolPhase("s1") {
		t.Fatal("StartToolPhase returned false")
	}
	m.AppendPending("s1", "补充 A")
	m.AppendPending("s1", "补充 B")

	pending, ok := m.CompleteLLM("s1")
	if !ok {
		t.Fatal("CompleteLLM returned false")
	}
	want := "补充信息：\n1. 补充 A\n2. 补充 B"
	if pending != want {
		t.Fatalf("pending = %q, want %q", pending, want)
	}
	if got := m.Snapshot("s1").Phase; got != PhaseIdle {
		t.Fatalf("phase = %s, want idle", got)
	}
}

func TestStopAllClearsTurns(t *testing.T) {
	m := NewManager()
	m.StartLLM("s1", "a")
	m.StartLLM("s2", "b")
	m.StopAll()
	if got := len(m.SnapshotAll()); got != 0 {
		t.Fatalf("turn count = %d, want 0", got)
	}
}

func TestFinishRequestClearsToolPending(t *testing.T) {
	m := NewManager()
	if !m.StartLLM("s1", "zz") {
		t.Fatal("StartLLM returned false")
	}
	if !m.StartToolPhase("s1") {
		t.Fatal("StartToolPhase returned false")
	}
	if !m.AppendPending("s1", "补充 A") {
		t.Fatal("AppendPending returned false")
	}

	m.FinishRequest("s1")
	if snapshot := m.Snapshot("s1"); snapshot.Phase != PhaseIdle || snapshot.PendingCount != 0 {
		t.Fatalf("snapshot = %#v, want idle without pending", snapshot)
	}
}

func TestFinishRequestKeepsAppendConfirmation(t *testing.T) {
	m := NewManager()
	m.StartLLM("s1", "zz")
	m.InterruptLLM("s1", "等等！")

	m.FinishRequest("s1")
	snapshot := m.Snapshot("s1")
	if snapshot.Phase != PhaseAwaitAppendConfirm || snapshot.PendingCount != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestFinishRequestStopsRiskConfirmation(t *testing.T) {
	m := NewManager()
	m.StartLLM("s1", "zz")
	m.StartToolPhase("s1")

	result := make(chan RiskConfirmationResponse, 1)
	okResult := make(chan bool, 1)
	go func() {
		resp, ok := m.AwaitRiskConfirmation("s1", RiskConfirmation{ID: "c1", ToolName: "shell"})
		result <- resp
		okResult <- ok
	}()

	waitForPhase(t, m, "s1", PhaseAwaitRiskConfirm)
	m.FinishRequest("s1")

	select {
	case resp := <-result:
		if !resp.Stopped {
			t.Fatalf("response = %#v, want stopped", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("AwaitRiskConfirmation was not released")
	}
	if ok := <-okResult; ok {
		t.Fatal("AwaitRiskConfirmation ok = true, want false")
	}
	if got := m.Snapshot("s1").Phase; got != PhaseIdle {
		t.Fatalf("phase = %s, want idle", got)
	}
}

func waitForPhase(t *testing.T, m *Manager, sessionID string, phase Phase) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := m.Snapshot(sessionID).Phase; got == phase {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("phase did not become %s", phase)
}
