package cli

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"elbot/internal/completion"
)

type fakeCompletingHandler struct {
	candidates []string
}

func (h fakeCompletingHandler) HandleMessage(context.Context, string) error { return nil }
func (h fakeCompletingHandler) Complete(string) []string                    { return h.candidates }

func TestCompleteInputCyclesCandidates(t *testing.T) {
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/chat", "/checkmodel"}}, width: 80, height: 20}
	m.input.SetValue("/c")

	updated, _ := m.completeInput()
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

	updated, _ = m.completeInput()
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/checkmodel" {
		t.Fatalf("second completion = %q", got)
	}

	updated, _ = m.completeInput()
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/chat" {
		t.Fatalf("cycled completion = %q", got)
	}
}

func TestCompletionSelectionUsesArrowKeysWhenPopupVisible(t *testing.T) {
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/chat", "/checkmodel"}}, width: 80, height: 20}
	m.input.SetValue("/c")
	updated, _ := m.completeInput()
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
	updated, _ := m.completeInput()
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/service" {
		t.Fatalf("completion = %q", got)
	}
}

func TestCancelKeyClearsCompletionOrInputBeforeQuit(t *testing.T) {
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/chat", "/checkmodel"}}, width: 80, height: 20}
	m.input.SetValue("/c")
	updated, _ := m.completeInput()
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
	if cmd == nil {
		t.Fatal("esc on empty input should quit")
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
