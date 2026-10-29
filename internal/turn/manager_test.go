package turn

import (
	"strings"
	"testing"

	"elbot/internal/llm"
)

func TestMergedInputRetainsElyphRequirement(t *testing.T) {
	for _, inputs := range [][]Input{
		{{Text: "task", NeedsElyphRules: true}},
		{{Text: "plain"}, {Text: "task", NeedsElyphRules: true}},
		{{Text: "task", NeedsElyphRules: true}, {Text: "plain"}},
	} {
		m := NewSupervisor()
		startTestExecution(t, m, "s", Input{})
		m.StartToolPhase("s", "attempt")
		for _, input := range inputs {
			m.AppendPendingInput("s", input)
		}
		merged := m.DrainMergedInput("s", "attempt")
		if !merged.NeedsElyphRules || !strings.Contains(merged.Text, "task") {
			t.Fatalf("lost queued requirement: %+v", merged)
		}
	}
	m := NewSupervisor()
	startTestExecution(t, m, "s", Input{})
	m.StartToolPhase("s", "attempt")
	m.AppendPendingInput("s", Input{Text: "plain"})
	if m.DrainMergedInput("s", "attempt").NeedsElyphRules {
		t.Fatal("plain input requested ELyph")
	}
}

func TestDrainMergedInputPreservesMediaAndPlatformText(t *testing.T) {
	m := NewSupervisor()
	startTestExecution(t, m, "s1", Input{Text: "start", PlatformText: "raw start", Segments: llm.TextSegments("start")})
	if !m.StartToolPhase("s1", "attempt") {
		t.Fatal("failed to start tool turn")
	}
	m.AppendPendingInput("s1", Input{Text: "first", PlatformText: "raw first", Segments: []llm.MessageSegment{{Type: llm.SegmentText, Text: "first"}, {Type: llm.SegmentImage, URL: "image-1"}}})
	m.AppendPendingInput("s1", Input{Text: "second", PlatformText: "raw second", Segments: []llm.MessageSegment{{Type: llm.SegmentText, Text: "second"}, {Type: llm.SegmentImage, URL: "image-2"}}})
	merged := m.DrainMergedInput("s1", "attempt")
	if merged.Text != "补充信息：\n1. first\n2. second" || merged.PlatformText != "补充信息：\n1. raw first\n2. raw second" {
		t.Fatalf("merged = %#v", merged)
	}
	var images int
	for _, segment := range merged.Segments {
		if segment.Type == llm.SegmentImage {
			images++
		}
	}
	if images != 2 {
		t.Fatalf("segments = %#v", merged.Segments)
	}
}

func TestInterruptAndConfirmMergesOriginalInput(t *testing.T) {
	m := NewSupervisor()
	e := startTestExecution(t, m, "s1", Input{Text: "zz"})
	if m.BeginAppend("s1", "attempt", Input{Text: "等等！"}) == nil {
		t.Fatal("InterruptLLMInput returned false")
	}
	if !m.AppendPendingInput("s1", Input{Text: "再补一句"}) {
		t.Fatal("AppendPendingInput returned false")
	}

	snapshot := m.Snapshot("s1")
	if snapshot.Phase != PhaseAwaitAppendConfirm || snapshot.PendingCount != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	m.FinishAttempt("s1", "attempt")
	merged, execution, ok := m.ResumeAppend("s1")
	if !ok || execution != e {
		t.Fatal("ResumeAppend did not preserve execution")
	}
	want := "补充信息：\n1. zz\n2. 等等！\n3. 再补一句"
	if merged.Text != want {
		t.Fatalf("merged = %q, want %q", merged.Text, want)
	}
	if got := m.Snapshot("s1"); got.Phase != PhaseLLM || got.AttemptDone != nil || got.PendingCount != 0 {
		t.Fatalf("reserved continuation = %+v", got)
	}
	if !m.StartAttempt("s1", merged, e, "next-attempt") {
		t.Fatal("failed to resume execution")
	}
	m.FinishAttempt("s1", "attempt")
	if m.Snapshot("s1").Attempt != "next-attempt" {
		t.Fatal("old completion removed continuation")
	}
}

func TestCancelAppendWaitsForAttempt(t *testing.T) {
	m := NewSupervisor()
	startTestExecution(t, m, "s1", Input{Text: "zz"})
	m.BeginAppend("s1", "attempt", Input{Text: "等等！"})

	if !m.CancelAppend("s1") {
		t.Fatal("CancelAppend returned false")
	}
	if got := m.Snapshot("s1").Phase; got != PhaseStopping {
		t.Fatalf("phase = %s, want stopping", got)
	}
	m.FinishAttempt("s1", "attempt")
	if got := m.Snapshot("s1").Phase; got != PhaseIdle {
		t.Fatalf("phase = %s, want idle", got)
	}
}

func TestToolPendingDrain(t *testing.T) {
	m := NewSupervisor()
	startTestExecution(t, m, "s1", Input{})
	m.StartToolPhase("s1", "attempt")
	m.AddToolUse("s1", "web_search", "attempt")
	m.AddToolUse("s1", "web_search", "attempt")
	if !m.AppendPendingInput("s1", Input{Text: "补充 A"}) {
		t.Fatal("AppendPendingInput returned false")
	}
	if !m.AppendPendingInput("s1", Input{Text: "补充 B"}) {
		t.Fatal("AppendPendingInput returned false")
	}

	snapshot := m.Snapshot("s1")
	if snapshot.Phase != PhaseTool || snapshot.PendingCount != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if got := snapshot.Tools["web_search"]; got != 2 {
		t.Fatalf("tool count = %d", got)
	}

	merged := m.DrainMergedInput("s1", "attempt")
	want := "补充信息：\n1. 补充 A\n2. 补充 B"
	if merged.Text != want {
		t.Fatalf("merged = %q, want %q", merged.Text, want)
	}
	if got := m.Snapshot("s1").PendingCount; got != 0 {
		t.Fatalf("pending = %d, want 0", got)
	}
}

func TestCompleteLLMReturnsMergedPending(t *testing.T) {
	m := NewSupervisor()
	e := startTestExecution(t, m, "s1", Input{Text: "先查"})
	if !m.StartToolPhase("s1", "attempt") {
		t.Fatal("StartToolPhase returned false")
	}
	m.AppendPendingInput("s1", Input{Text: "补充 A"})
	m.AppendPendingInput("s1", Input{Text: "补充 B"})

	pending, ok := m.CompleteTurn("s1", "attempt")
	if !ok {
		t.Fatal("CompleteTurn returned false")
	}
	want := "补充信息：\n1. 补充 A\n2. 补充 B"
	if pending.Text != want {
		t.Fatalf("pending = %q, want %q", pending.Text, want)
	}
	if got := m.Snapshot("s1"); got.Phase != PhaseLLM || got.AttemptDone != nil || got.Execution != e || !m.CanCompact("s1", e) {
		t.Fatalf("pending continuation = %+v", got)
	}
}

func TestStopAllWaitsForAttempts(t *testing.T) {
	m := NewSupervisor()
	startTestExecution(t, m, "s1", Input{Text: "a"})
	startTestExecution(t, m, "s2", Input{Text: "b"})
	m.StopAll()
	for _, snapshot := range m.SnapshotAll() {
		if snapshot.Phase != PhaseStopping {
			t.Fatalf("stop released active attempt: %+v", snapshot)
		}
	}
	m.FinishAttempt("s1", "attempt")
	m.FinishAttempt("s2", "attempt")
	if got := len(m.SnapshotAll()); got != 0 {
		t.Fatalf("turn count = %d, want 0", got)
	}
}

func TestFinishAttemptClearsToolPending(t *testing.T) {
	m := NewSupervisor()
	startTestExecution(t, m, "s1", Input{Text: "zz"})
	if !m.StartToolPhase("s1", "attempt") {
		t.Fatal("StartToolPhase returned false")
	}
	if !m.AppendPendingInput("s1", Input{Text: "补充 A"}) {
		t.Fatal("AppendPendingInput returned false")
	}

	m.FinishAttempt("s1", "attempt")
	if snapshot := m.Snapshot("s1"); snapshot.Phase != PhaseIdle || snapshot.PendingCount != 0 {
		t.Fatalf("snapshot = %#v, want idle without pending", snapshot)
	}
}

func TestFinishAttemptKeepsAppendConfirmation(t *testing.T) {
	m := NewSupervisor()
	startTestExecution(t, m, "s1", Input{Text: "zz"})
	m.BeginAppend("s1", "attempt", Input{Text: "等等！"})

	m.FinishAttempt("s1", "attempt")
	snapshot := m.Snapshot("s1")
	if snapshot.Phase != PhaseAwaitAppendConfirm || snapshot.PendingCount != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
func startTestExecution(t *testing.T, m *Supervisor, sessionID string, input Input) *Execution {
	t.Helper()
	e := NewExecution(sessionID + "-run")
	if !m.StartAttempt(sessionID, input, e, "attempt") {
		t.Fatal("failed to start execution")
	}
	t.Cleanup(func() {
		m.StopSession(sessionID)
		m.FinishAttempt(sessionID, "attempt")
	})
	return e
}
