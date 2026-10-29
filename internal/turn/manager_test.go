// 待改动：覆盖输入合并、工具计数、阶段流转及确认生命周期。
// 随职责拆分迁移测试，使用实际 Execution 与 attempt 流程替代旧入口。
package turn

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/llm"
)

func TestMergedInputRetainsElyphRequirement(t *testing.T) {
	for _, inputs := range [][]Input{
		{{Text: "task", NeedsElyphRules: true}},
		{{Text: "plain"}, {Text: "task", NeedsElyphRules: true}},
		{{Text: "task", NeedsElyphRules: true}, {Text: "plain"}},
	} {
		m := NewManager()
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
	m := NewManager()
	startTestExecution(t, m, "s", Input{})
	m.StartToolPhase("s", "attempt")
	m.AppendPendingInput("s", Input{Text: "plain"})
	if m.DrainMergedInput("s", "attempt").NeedsElyphRules {
		t.Fatal("plain input requested ELyph")
	}
}

func TestDrainMergedInputPreservesMediaAndPlatformText(t *testing.T) {
	m := NewManager()
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
	e := startTestExecution(t, m, "s1", Input{Text: "zz"})
	if !m.InterruptLLMInput("s1", Input{Text: "等等！"}) {
		t.Fatal("InterruptLLMInput returned false")
	}
	if !m.AppendPendingInput("s1", Input{Text: "再补一句"}) {
		t.Fatal("AppendPendingInput returned false")
	}

	snapshot := m.Snapshot("s1")
	if snapshot.Phase != PhaseAwaitAppendConfirm || snapshot.PendingCount != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	m.FinishRequest("s1", "attempt")
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
	if !m.StartExecution("s1", merged, e, "next-attempt") {
		t.Fatal("failed to resume execution")
	}
	m.FinishRequest("s1", "attempt")
	if !m.MatchesAttempt("s1", "next-attempt") {
		t.Fatal("old completion removed continuation")
	}
}

func TestCancelAppendWaitsForAttempt(t *testing.T) {
	m := NewManager()
	startTestExecution(t, m, "s1", Input{Text: "zz"})
	m.InterruptLLMInput("s1", Input{Text: "等等！"})

	if !m.CancelAppend("s1") {
		t.Fatal("CancelAppend returned false")
	}
	if got := m.Snapshot("s1").Phase; got != PhaseStopping {
		t.Fatalf("phase = %s, want stopping", got)
	}
	m.FinishRequest("s1", "attempt")
	if got := m.Snapshot("s1").Phase; got != PhaseIdle {
		t.Fatalf("phase = %s, want idle", got)
	}
}

func TestToolPendingDrain(t *testing.T) {
	m := NewManager()
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
	if got := ToolsString(snapshot.Tools); got != "web_search x2" {
		t.Fatalf("ToolsString = %q", got)
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
	m := NewManager()
	e := startTestExecution(t, m, "s1", Input{Text: "先查"})
	if !m.StartToolPhase("s1", "attempt") {
		t.Fatal("StartToolPhase returned false")
	}
	m.AppendPendingInput("s1", Input{Text: "补充 A"})
	m.AppendPendingInput("s1", Input{Text: "补充 B"})

	pending, ok := m.CompleteLLMInput("s1", "attempt")
	if !ok {
		t.Fatal("CompleteLLMInput returned false")
	}
	want := "补充信息：\n1. 补充 A\n2. 补充 B"
	if pending.Text != want {
		t.Fatalf("pending = %q, want %q", pending.Text, want)
	}
	if got := m.Snapshot("s1"); got.Phase != PhaseLLM || got.AttemptDone != nil || m.Execution("s1") != e || !m.CanCompact("s1", e) {
		t.Fatalf("pending continuation = %+v", got)
	}
}

func TestStopAllWaitsForAttempts(t *testing.T) {
	m := NewManager()
	startTestExecution(t, m, "s1", Input{Text: "a"})
	startTestExecution(t, m, "s2", Input{Text: "b"})
	m.StopAll()
	for _, snapshot := range m.SnapshotAll() {
		if snapshot.Phase != PhaseStopping {
			t.Fatalf("stop released active attempt: %+v", snapshot)
		}
	}
	m.FinishRequest("s1", "attempt")
	m.FinishRequest("s2", "attempt")
	if got := len(m.SnapshotAll()); got != 0 {
		t.Fatalf("turn count = %d, want 0", got)
	}
}

func TestFinishRequestClearsToolPending(t *testing.T) {
	m := NewManager()
	startTestExecution(t, m, "s1", Input{Text: "zz"})
	if !m.StartToolPhase("s1", "attempt") {
		t.Fatal("StartToolPhase returned false")
	}
	if !m.AppendPendingInput("s1", Input{Text: "补充 A"}) {
		t.Fatal("AppendPendingInput returned false")
	}

	m.FinishRequest("s1", "attempt")
	if snapshot := m.Snapshot("s1"); snapshot.Phase != PhaseIdle || snapshot.PendingCount != 0 {
		t.Fatalf("snapshot = %#v, want idle without pending", snapshot)
	}
}

func TestFinishRequestKeepsAppendConfirmation(t *testing.T) {
	m := NewManager()
	startTestExecution(t, m, "s1", Input{Text: "zz"})
	m.InterruptLLMInput("s1", Input{Text: "等等！"})

	m.FinishRequest("s1", "attempt")
	snapshot := m.Snapshot("s1")
	if snapshot.Phase != PhaseAwaitAppendConfirm || snapshot.PendingCount != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestFinishRequestStopsRiskConfirmation(t *testing.T) {
	m := NewManager()
	startTestExecution(t, m, "s1", Input{Text: "zz"})
	m.StartToolPhase("s1", "attempt")

	wait := m.BeginRiskConfirmation("s1", RiskConfirmation{ID: "c1", ToolName: "shell"}, "attempt")
	result := make(chan RiskConfirmationResponse, 1)
	okResult := make(chan bool, 1)
	go func() {
		resp, ok, _ := wait.Wait(context.Background(), 0)
		result <- resp
		okResult <- ok
	}()

	m.FinishRequest("s1", "attempt")

	select {
	case resp := <-result:
		if !resp.Stopped {
			t.Fatalf("response = %#v, want stopped", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("risk confirmation wait was not released")
	}
	if ok := <-okResult; ok {
		t.Fatal("risk confirmation accepted after request finished")
	}
	if got := m.Snapshot("s1").Phase; got != PhaseIdle {
		t.Fatalf("phase = %s, want idle", got)
	}
}

func TestAppendConfirmationExpirationRefreshesOnContent(t *testing.T) {
	m := NewManager()
	startTestExecution(t, m, "s1", Input{Text: "start"})
	m.InterruptLLMInput("s1", Input{Text: "more"})
	m.FinishRequest("s1", "attempt")

	expired := make(chan bool, 1)
	wait := m.AppendWait("s1")
	go func() { expired <- wait.Wait(context.Background(), 80*time.Millisecond) }()
	time.Sleep(50 * time.Millisecond)
	if !m.AppendPendingInput("s1", Input{Text: "latest"}) {
		t.Fatal("append did not refresh confirmation wait")
	}
	time.Sleep(50 * time.Millisecond)
	if got := m.Snapshot("s1").Phase; got != PhaseAwaitAppendConfirm {
		t.Fatalf("phase after refresh = %s", got)
	}

	select {
	case ok := <-expired:
		if !ok {
			t.Fatal("append wait ended without expiring")
		}
	case <-time.After(time.Second):
		t.Fatal("append wait did not expire")
	}
	if got := m.Snapshot("s1").Phase; got != PhaseIdle {
		t.Fatalf("phase after expiry = %s", got)
	}
}

func TestExpiredAppendWaitCannotRemoveNewTurn(t *testing.T) {
	m := NewManager()
	startTestExecution(t, m, "s1", Input{Text: "first"})
	m.InterruptLLMInput("s1", Input{Text: "more"})
	oldWait := make(chan bool, 1)
	wait := m.AppendWait("s1")
	go func() { oldWait <- wait.Wait(context.Background(), 100*time.Millisecond) }()
	time.Sleep(10 * time.Millisecond)
	if !m.CancelAppend("s1") {
		t.Fatal("cancel append failed")
	}
	m.FinishRequest("s1", "attempt")
	startTestExecution(t, m, "s1", Input{Text: "second"})
	if !m.InterruptLLMInput("s1", Input{Text: "new more"}) {
		t.Fatal("failed to start replacement append wait")
	}

	select {
	case expired := <-oldWait:
		if expired {
			t.Fatal("canceled append wait reported expiry")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled append waiter was not released")
	}
	time.Sleep(110 * time.Millisecond)
	if got := m.Snapshot("s1").Phase; got != PhaseAwaitAppendConfirm {
		t.Fatalf("replacement phase = %s", got)
	}
}
func TestRiskConfirmationExpirationRefreshesOnDetail(t *testing.T) {
	m := NewManager()
	startTestExecution(t, m, "s1", Input{Text: "start"})
	m.StartToolPhase("s1", "attempt")

	wait := m.BeginRiskConfirmation("s1", RiskConfirmation{ID: "c1", ToolName: "shell"}, "attempt")
	result := make(chan RiskConfirmationResponse, 1)
	go func() {
		resp, _, _ := wait.Wait(context.Background(), 80*time.Millisecond)
		result <- resp
	}()
	time.Sleep(50 * time.Millisecond)
	if !m.RefreshRiskConfirmation("s1") {
		t.Fatal("detail did not refresh risk confirmation wait")
	}
	time.Sleep(50 * time.Millisecond)
	select {
	case resp := <-result:
		t.Fatalf("risk wait expired before refreshed deadline: %#v", resp)
	default:
	}

	select {
	case resp := <-result:
		if !resp.Stopped || !resp.Expired {
			t.Fatalf("response = %#v, want expired stop", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("risk confirmation did not expire")
	}
	if got := m.Snapshot("s1").Phase; got != PhaseStopping {
		t.Fatalf("phase after expiry = %s, want stopping", got)
	}
	m.FinishRequest("s1", "attempt")
}

func startTestExecution(t *testing.T, m *Manager, sessionID string, input Input) *Execution {
	t.Helper()
	e := NewExecution(sessionID + "-run")
	if !m.StartExecution(sessionID, input, e, "attempt") {
		t.Fatal("failed to start execution")
	}
	t.Cleanup(func() {
		m.StopSession(sessionID)
		m.FinishRequest(sessionID)
	})
	return e
}
