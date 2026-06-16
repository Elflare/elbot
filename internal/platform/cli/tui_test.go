package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"elbot/internal/completion"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
)

type fakeCompletingHandler struct {
	candidates []string
}

func (h fakeCompletingHandler) HandleMessage(context.Context, string) error { return nil }
func (h fakeCompletingHandler) Complete(string) []string                    { return h.candidates }

func TestCompleteInputCyclesCandidates(t *testing.T) {
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/chat", "/checkmodel"}}, width: 80, height: 20}
	m.input.SetValue("/c")

	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/chat" {
		t.Fatalf("first completion = %q", got)
	}
	if !m.completionState.visible() {
		t.Fatal("completion popup should be visible")
	}
	if strings.TrimSpace(m.content) != "" {
		t.Fatalf("completion candidates should not be printed to transcript: %q", m.content)
	}

	updated, _ = m.completeInput(1)
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/checkmodel" {
		t.Fatalf("second completion = %q", got)
	}

	updated, _ = m.completeInput(1)
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/chat" {
		t.Fatalf("cycled completion = %q", got)
	}
}

func TestCompletionSelectionUsesArrowKeysWhenPopupVisible(t *testing.T) {
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/chat", "/checkmodel"}}, width: 80, height: 20}
	m.input.SetValue("/c")
	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/checkmodel" {
		t.Fatalf("down completion = %q", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/chat" {
		t.Fatalf("up completion = %q", got)
	}
}

func TestCompletionServicePreferredOverLegacyHandler(t *testing.T) {
	service := completion.NewService(staticCompletionSource{{Text: "/service"}, {Text: "/service2"}})
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/legacy"}}, completion: service, width: 80, height: 20}
	m.input.SetValue("/s")
	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/service" {
		t.Fatalf("completion = %q", got)
	}
}

func TestCompletionShiftTabSelectsPreviousCandidate(t *testing.T) {
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/chat", "/checkmodel"}}, width: 80, height: 20}
	m.input.SetValue("/c")
	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/checkmodel" {
		t.Fatalf("shift+tab completion = %q", got)
	}
}

func TestCompletionSingleItemUsesReplaceRange(t *testing.T) {
	service := completion.NewService(staticCompletionSource{{Text: "openai/gpt-4o", ReplaceStart: len("/model "), ReplaceEnd: len("/model gp")}})
	m := tuiModel{completion: service, width: 80, height: 20}
	m.input.SetValue("/model gp")
	m.input.CursorEnd()

	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/model openai/gpt-4o" {
		t.Fatalf("single completion = %q", got)
	}
}

func TestCancelKeyClearsCompletionOrInputBeforeQuit(t *testing.T) {
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/chat", "/checkmodel"}}, width: 80, height: 20}
	m.input.SetValue("/c")
	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(tuiModel)
	if cmd != nil || m.completionState.visible() || m.input.Value() == "" {
		t.Fatalf("esc should close popup only, visible=%v input=%q cmd=%v", m.completionState.visible(), m.input.Value(), cmd)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(tuiModel)
	if cmd != nil || m.input.Value() != "" {
		t.Fatalf("ctrl+c should clear input only, input=%q cmd=%v", m.input.Value(), cmd)
	}

	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("esc on empty input should not quit")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c on empty input should quit")
	}
}

func TestRuntimeStatusTextShowsElapsedAndUsage(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := tuiModel{
		width:     100,
		statusNow: start.Add(8 * time.Second),
		runtimeStatus: runtimestatus.Snapshot{
			SessionID:      "s1",
			Phase:          runtimestatus.PhaseLLM,
			Provider:       "deepseek",
			Model:          "reasoner",
			TurnStartedAt:  start,
			StageStartedAt: start,
			Usage:          &llm.Usage{TotalTokens: 18240, CacheHitTokens: 12100},
		},
	}
	got := m.runtimeStatusText()
	for _, want := range []string{"llm", "00:08", "deepseek/reasoner", "tokens 18,240", "cache 12,100"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status %q missing %q", got, want)
		}
	}
}

func TestRuntimeStatusTickUpdatesElapsed(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := tuiModel{width: 80, statusNow: start.Add(time.Second), runtimeStatus: runtimestatus.Snapshot{SessionID: "s1", Phase: runtimestatus.PhaseLLM, TurnStartedAt: start, StageStartedAt: start}}
	before := m.runtimeStatusText()
	updated, _ := m.Update(tuiStatusTickMsg(start.Add(2 * time.Second)))
	m = updated.(tuiModel)
	after := m.runtimeStatusText()
	if before == after || !strings.Contains(after, "00:02") {
		t.Fatalf("status did not update, before=%q after=%q", before, after)
	}
}

type staticCompletionSource []completion.Item

func (s staticCompletionSource) Complete(context.Context, completion.Request) []completion.Item {
	return s
}

func TestAppendAssistantContentDoesNotAddSeparatorOnNewline(t *testing.T) {
	m := &tuiModel{assistantName: "assistant"}
	m.appendAssistantContent("第一句\n")
	m.appendAssistantContent("第二句")
	got := m.content
	if strings.Contains(got, "---") {
		t.Fatalf("unexpected separator in content: %q", got)
	}
	if !strings.Contains(got, "assistant: 第一句\n第二句") {
		t.Fatalf("assistant content was not kept in one block: %q", got)
	}
}

func TestReplaceAssistantContentReplacesCurrentBlock(t *testing.T) {
	m := &tuiModel{userName: "user", assistantName: "assistant"}
	m.appendUserContent("hello")
	m.appendAssistantContent("raw ")
	m.appendAssistantContent("[[token]]")
	m.replaceAssistantContent("final text")

	got := m.content
	if !strings.Contains(got, "assistant: final text") {
		t.Fatalf("missing final text: %q", got)
	}
	if strings.Contains(got, "raw") || strings.Contains(got, "[[token]]") {
		t.Fatalf("raw streamed text was not replaced: %q", got)
	}
}

func TestReasoningContentIsSeparateFromAssistantReplace(t *testing.T) {
	m := &tuiModel{userName: "user", assistantName: "assistant"}
	m.appendUserContent("hello")
	m.appendReasoningContent("[thinking] ")
	m.appendReasoningContent("先分析")
	m.appendReasoningContent("[/thinking]\n\n")
	m.appendAssistantContent("raw")
	m.replaceAssistantContent("final text")

	got := m.content
	if !strings.Contains(got, "thinking: 先分析") {
		t.Fatalf("missing reasoning content: %q", got)
	}
	if !strings.Contains(got, "assistant: final text") {
		t.Fatalf("missing final assistant content: %q", got)
	}
	if strings.Contains(got, "[thinking]") || strings.Contains(got, "[/thinking]") {
		t.Fatalf("thinking markers should not be displayed: %q", got)
	}
	if strings.Contains(got, "assistant: raw") {
		t.Fatalf("raw assistant content was not replaced: %q", got)
	}
}

func TestReasoningRenderKeepsContentAcrossBlankLines(t *testing.T) {
	m := &tuiModel{assistantName: "assistant"}
	m.appendReasoningContent("[thinking] 第一段\n\n第二段")
	rendered := m.renderContent(m.content)
	if !strings.Contains(rendered, "thinking: 第一段\n\n第二段") {
		t.Fatalf("reasoning paragraphs were not preserved: %q", rendered)
	}
	if strings.Contains(rendered, "[thinking]") || strings.Contains(rendered, "[/thinking]") {
		t.Fatalf("thinking markers should not be displayed: %q", rendered)
	}
}

func TestReasoningSurvivesNoticeFallbackAndAssistantReplace(t *testing.T) {
	m := &tuiModel{assistantName: "assistant"}
	m.appendReasoningContent("[thinking] 先想\n\n再想")
	m.appendContent("[notice] [tool] 正在调用 web_search\n")
	m.appendAssistantContent("raw")
	m.replaceAssistantContent("final text")

	got := m.content
	for _, want := range []string{"thinking: 先想", "再想", "[notice] [tool] 正在调用 web_search", "assistant: final text"} {
		if !strings.Contains(got, want) {
			t.Fatalf("content %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "assistant: raw") {
		t.Fatalf("raw assistant content was not replaced: %q", got)
	}
}

func TestFinishAssistantContentStartsNextAssistantBlock(t *testing.T) {
	m := &tuiModel{assistantName: "assistant"}
	m.appendAssistantContent("first")
	m.finishAssistantContent()
	m.appendAssistantContent("second")

	got := m.content
	if strings.Count(got, "assistant: ") != 2 {
		t.Fatalf("expected two assistant blocks: %q", got)
	}
}

func TestAppendUserContentAddsSeparatorBetweenTurns(t *testing.T) {
	m := &tuiModel{userName: "user", assistantName: "assistant", width: 12}
	m.appendUserContent("第一轮")
	m.appendAssistantContent("回答")
	m.appendUserContent("第二轮")
	got := m.content
	if !strings.Contains(got, "────────────") {
		t.Fatalf("missing separator line: %q", got)
	}
	if strings.Contains(got, "---") {
		t.Fatalf("unexpected plain separator: %q", got)
	}
}
