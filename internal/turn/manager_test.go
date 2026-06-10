package turn

import "testing"

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

func TestStopAllClearsTurns(t *testing.T) {
	m := NewManager()
	m.StartLLM("s1", "a")
	m.StartLLM("s2", "b")
	m.StopAll()
	if got := len(m.SnapshotAll()); got != 0 {
		t.Fatalf("turn count = %d, want 0", got)
	}
}
