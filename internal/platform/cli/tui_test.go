package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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

type capturingHandler struct {
	messages chan string
}

func (h capturingHandler) HandleMessage(_ context.Context, text string) error {
	h.messages <- text
	return nil
}

func TestTUIInputCentersPromptAndSingleLine(t *testing.T) {
	m := tuiModel{input: newTUIInput(), width: 20, height: 20}
	m.input.SetValue("hello")
	m.resizeViewports()

	view := m.inputView()
	if got := strings.Count(view, "❯"); got != 1 {
		t.Fatalf("prompt count = %d, want 1", got)
	}
	lines := strings.Split(view, "\n")
	if len(lines) != minTUIInputHeight {
		t.Fatalf("input view height = %d, want %d", len(lines), minTUIInputHeight)
	}
	if !strings.Contains(lines[1], "❯") || !strings.Contains(lines[1], "hello") {
		t.Fatalf("center line = %q, want prompt and input text", lines[1])
	}
	if got, want := m.input.FocusedStyle.CursorLine.GetBackground(), m.input.FocusedStyle.Text.GetBackground(); got != want {
		t.Fatalf("cursor line background = %v, want text background %v", got, want)
	}
}

func TestTUIInputCentersMultilineBlock(t *testing.T) {
	m := tuiModel{input: newTUIInput(), width: 20, height: 20}
	m.input.SetValue("first\nsecond")
	m.resizeViewports()

	lines := strings.Split(m.inputView(), "\n")
	if len(lines) != minTUIInputHeight {
		t.Fatalf("input view height = %d, want %d", len(lines), minTUIInputHeight)
	}
	if !strings.Contains(lines[0], "first") || !strings.Contains(lines[1], "second") {
		t.Fatalf("multiline input is not centered as a block: %q", lines)
	}
	if !strings.Contains(lines[1], "❯") {
		t.Fatalf("prompt is not vertically centered: %q", lines)
	}
}

func TestTUIInputSeparatorIsCenteredAndThreeQuarterWidth(t *testing.T) {
	m := tuiModel{width: 80}
	view := m.inputSeparatorView()
	wantWidth := m.width * 3 / 4
	if got := strings.Count(view, "─"); got != wantWidth {
		t.Fatalf("separator width = %d, want %d", got, wantWidth)
	}
	if wantPadding := (m.width - wantWidth) / 2; !strings.HasPrefix(view, strings.Repeat(" ", wantPadding)) {
		t.Fatalf("separator is not centered: %q", view)
	}
}

func TestCompleteInputCyclesCandidates(t *testing.T) {
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/chat", "/checkmodel"}}, input: newTUIInput(), width: 80, height: 20}
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
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/chat", "/checkmodel"}}, input: newTUIInput(), width: 80, height: 20}
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
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/legacy"}}, completion: service, input: newTUIInput(), width: 80, height: 20}
	m.input.SetValue("/s")
	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/service" {
		t.Fatalf("completion = %q", got)
	}
}

func TestCompletionShiftTabSelectsPreviousCandidate(t *testing.T) {
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/chat", "/checkmodel"}}, input: newTUIInput(), width: 80, height: 20}
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
	m := tuiModel{completion: service, input: newTUIInput(), width: 80, height: 20}
	m.input.SetValue("/model gp")
	m.input.CursorEnd()

	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "/model openai/gpt-4o" {
		t.Fatalf("single completion = %q", got)
	}
}

func TestCompletionUsesByteRangesWithFullWidthInput(t *testing.T) {
	service := completion.NewService(staticCompletionSource{{Text: "@t：web_search", ReplaceStart: 0, ReplaceEnd: len("@t：we")}})
	m := tuiModel{completion: service, input: newTUIInput(), width: 80, height: 20}
	m.input.SetValue("@t：we")
	m.input.CursorEnd()

	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "@t：web_search" {
		t.Fatalf("single completion = %q", got)
	}
	if got := textareaCursorRunePosition(m.input); got != len([]rune("@t：web_search")) {
		t.Fatalf("cursor position = %d", got)
	}
}

func TestCompletionUsesCursorInMultilineInput(t *testing.T) {
	prefix := "context\n"
	value := prefix + "/model gp"
	service := completion.NewService(staticCompletionSource{{
		Text:         "openai/gpt-4o",
		ReplaceStart: len(prefix + "/model "),
		ReplaceEnd:   len(value),
	}})
	m := tuiModel{completion: service, input: newTUIInput(), width: 80, height: 20}
	m.input.SetValue(value)

	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)
	want := prefix + "/model openai/gpt-4o"
	if got := m.input.Value(); got != want {
		t.Fatalf("multiline completion = %q", got)
	}
	if got := textareaCursorRunePosition(m.input); got != len([]rune(want)) {
		t.Fatalf("cursor position = %d", got)
	}
}

func TestLocalFileCompletionFuzzyMatchesRelativePaths(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "internal/app/app.go", "package app\n")
	mustWriteFile(t, root, "README.md", "hello\n")
	m := tuiModel{localFiles: newLocalFileResolver(root), input: newTUIInput(), width: 80, height: 20}
	m.input.SetValue("#iag")
	m.input.CursorEnd()

	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "#internal/app/app.go" {
		t.Fatalf("local file completion = %q", got)
	}
}

func TestLocalFileCompletionQuotesPathsWithSpaces(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "notes/a b.txt", "hello\n")
	m := tuiModel{localFiles: newLocalFileResolver(root), input: newTUIInput(), width: 80, height: 20}
	m.input.SetValue("#\"ab")
	m.input.CursorEnd()

	updated, _ := m.completeInput(1)
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "#\"notes/a b.txt\"" {
		t.Fatalf("quoted local file completion = %q", got)
	}
}

func TestEnterExpandsLocalFileReferencesBeforeSending(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "foo.txt", "hello\n")
	handler := capturingHandler{messages: make(chan string, 1)}
	m := tuiModel{ctx: context.Background(), handler: handler, output: make(chan tea.Msg, 1), localFiles: newLocalFileResolver(root), input: newTUIInput(), width: 80, height: 20}
	m.input.SetValue("see #foo.txt")
	m.input.CursorEnd()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	select {
	case got := <-handler.messages:
		if !strings.Contains(got, "[file: foo.txt]") || !strings.Contains(got, "hello\n") {
			t.Fatalf("expanded message = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("message was not sent")
	}
	if len(m.history) != 1 || m.history[0] != "see #foo.txt" {
		t.Fatalf("history = %#v", m.history)
	}
	if !strings.Contains(m.content, "see #foo.txt") {
		t.Fatalf("transcript = %q", m.content)
	}
}

func TestEnterRestoresInputWhenLocalFileReferenceFails(t *testing.T) {
	root := t.TempDir()
	handler := capturingHandler{messages: make(chan string, 1)}
	m := tuiModel{ctx: context.Background(), handler: handler, output: make(chan tea.Msg, 1), localFiles: newLocalFileResolver(root), input: newTUIInput(), width: 80, height: 20}
	m.input.SetValue("see #missing.txt")
	m.input.CursorEnd()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "see #missing.txt" {
		t.Fatalf("input after failed expansion = %q", got)
	}
	if len(m.history) != 0 {
		t.Fatalf("history should remain empty: %#v", m.history)
	}
	if !strings.Contains(m.content, "local file reference:") {
		t.Fatalf("notice missing from transcript: %q", m.content)
	}
	select {
	case got := <-handler.messages:
		t.Fatalf("message should not be sent: %q", got)
	default:
	}
}

func TestCancelKeyClearsCompletionOrInputBeforeQuit(t *testing.T) {
	m := tuiModel{handler: fakeCompletingHandler{candidates: []string{"/chat", "/checkmodel"}}, input: newTUIInput(), width: 80, height: 20}
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

func TestAppendNoticeContentUsesSeparator(t *testing.T) {
	m := tuiModel{width: 80}
	m.appendUserContent("hello")
	m.appendNotice("notice one")

	if !strings.Contains(m.content, "\n"+m.separatorLine()+"\n[notice] notice one") {
		t.Fatalf("notice content not separated:\n%s", m.content)
	}
	if strings.HasSuffix(m.content, "\n") {
		t.Fatalf("notice content should not end with newline: %q", m.content)
	}
}

func TestRefreshNoticesUsesSeparator(t *testing.T) {
	m := tuiModel{input: newTUIInput(), width: 120, height: 20}
	m.resizeViewports()
	m.notices = []string{"notice one", "notice two"}
	m.refreshNotices()

	got := m.noticeViewport.View()
	if !strings.Contains(got, "notice one") || !strings.Contains(got, "notice two") {
		t.Fatalf("notice view missing notices:\n%s", got)
	}
	if !strings.Contains(got, strings.Repeat("─", 8)) {
		t.Fatalf("notice view missing separator:\n%s", got)
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

func TestRuntimeStatusTickKeepsFinishedElapsedFixed(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	finished := start.Add(3 * time.Second)
	m := tuiModel{
		width:     80,
		statusNow: finished,
		runtimeStatus: runtimestatus.Snapshot{
			SessionID:     "s1",
			Phase:         runtimestatus.PhaseDone,
			TurnStartedAt: start,
			FinishedAt:    finished,
		},
	}
	before := m.runtimeStatusText()
	updated, _ := m.Update(tuiStatusTickMsg(finished.Add(time.Minute)))
	after := updated.(tuiModel).runtimeStatusText()
	if before != after || !strings.Contains(after, "00:03") {
		t.Fatalf("finished status changed, before=%q after=%q", before, after)
	}
}

func TestMultilinePasteStaysInTextareaUntilEnter(t *testing.T) {
	handler := capturingHandler{messages: make(chan string, 1)}
	m := tuiModel{
		ctx:        context.Background(),
		handler:    handler,
		output:     make(chan tea.Msg, 1),
		localFiles: newLocalFileResolver(t.TempDir()),
		input:      newTUIInput(),
		width:      80,
		height:     20,
		userName:   "user",
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first\r\nsecond"), Paste: true})
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "first\nsecond" {
		t.Fatalf("textarea value = %q", got)
	}
	if m.input.LineCount() != 2 || m.input.Line() != 1 {
		t.Fatalf("textarea cursor line=%d lines=%d", m.input.Line(), m.input.LineCount())
	}
	select {
	case got := <-handler.messages:
		t.Fatalf("paste sent before enter: %q", got)
	default:
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	select {
	case got := <-handler.messages:
		if got != "first\nsecond" {
			t.Fatalf("sent paste = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("pasted text was not sent")
	}
	if !strings.Contains(m.content, "first\nsecond") {
		t.Fatalf("transcript did not show full paste: %q", m.content)
	}
}

func TestClipboardPasteNormalizesCRLF(t *testing.T) {
	m := tuiModel{input: newTUIInput(), width: 80, height: 20}

	updated, _ := m.Update(tuiClipboardMsg{text: "你好\r\n世界"})
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "你好\n世界" {
		t.Fatalf("clipboard paste = %q", got)
	}
}

func TestWindowsPasteBurstEnterInsertsNewlineBeforeSubmit(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows console input behavior")
	}
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	handler := capturingHandler{messages: make(chan string, 1)}
	m := tuiModel{
		ctx:      context.Background(),
		handler:  handler,
		output:   make(chan tea.Msg, 1),
		input:    newTUIInput(),
		inputNow: func() time.Time { return current },
		width:    80,
		height:   20,
	}
	update := func(msg tea.KeyMsg) {
		current = current.Add(time.Millisecond)
		updated, _ := m.Update(msg)
		m = updated.(tuiModel)
	}
	for _, r := range "abc" {
		update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	update(tea.KeyMsg{Type: tea.KeyEnter})
	for _, r := range "def" {
		update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := m.input.Value(); got != "abc\ndef" {
		t.Fatalf("paste burst value = %q", got)
	}
	select {
	case got := <-handler.messages:
		t.Fatalf("paste burst sent early: %q", got)
	default:
	}

	current = current.Add(pasteBurstIdleTimeout + time.Millisecond)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	select {
	case got := <-handler.messages:
		if got != "abc\ndef" {
			t.Fatalf("sent paste burst = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("paste burst was not sent")
	}
}

func TestEnterInPreviousLineInsertsNewline(t *testing.T) {
	handler := capturingHandler{messages: make(chan string, 1)}
	m := tuiModel{ctx: context.Background(), handler: handler, input: newTUIInput(), width: 80, height: 20}
	setTextareaValueAndCursor(&m.input, "first\nsecond", len([]rune("first")))

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if got := m.input.Value(); got != "first\n\nsecond" {
		t.Fatalf("textarea value = %q", got)
	}
	select {
	case got := <-handler.messages:
		t.Fatalf("middle-line enter sent message: %q", got)
	default:
	}
}

func TestUpDownUseHistoryOnlyAtTextareaBoundaries(t *testing.T) {
	m := tuiModel{input: newTUIInput(), history: []string{"older", "newer"}, histPos: 2, width: 80, height: 20}
	m.input.SetValue("top\nbottom")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(tuiModel)
	if m.input.Value() != "top\nbottom" || m.input.Line() != 0 {
		t.Fatalf("up inside textarea changed history: value=%q line=%d", m.input.Value(), m.input.Line())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(tuiModel)
	if m.input.Value() != "newer" {
		t.Fatalf("up at first line = %q", m.input.Value())
	}

	m.input.SetValue("top\nbottom")
	setTextareaValueAndCursor(&m.input, m.input.Value(), 0)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(tuiModel)
	if m.input.Value() != "top\nbottom" || m.input.Line() != 1 {
		t.Fatalf("down inside textarea changed history: value=%q line=%d", m.input.Value(), m.input.Line())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(tuiModel)
	if m.input.Value() != "" {
		t.Fatalf("down at last line = %q", m.input.Value())
	}
}

func TestTextareaHeightIsCapped(t *testing.T) {
	m := tuiModel{input: newTUIInput(), width: 80, height: 30}
	m.input.SetValue(strings.Repeat("line\n", 9) + "line")
	m.resizeViewports()
	if got := m.input.Height(); got != maxTUIInputHeight {
		t.Fatalf("input height = %d", got)
	}
	if got, want := m.viewport.Height, 30-4-maxTUIInputHeight; got != want {
		t.Fatalf("viewport height = %d, want %d", got, want)
	}
}

func TestTextareaHasMinimumHeight(t *testing.T) {
	m := tuiModel{input: newTUIInput(), width: 80, height: 30}
	m.input.SetValue("one line")
	m.resizeViewports()
	if got := m.input.Height(); got != 1 {
		t.Fatalf("textarea height = %d, want 1", got)
	}
	if got, want := m.inputViewHeight(), minTUIInputHeight; got != want {
		t.Fatalf("input view height = %d, want %d", got, want)
	}
	if got, want := m.viewport.Height, 30-4-minTUIInputHeight; got != want {
		t.Fatalf("viewport height = %d, want %d", got, want)
	}
}

func TestTextareaPasteHonorsCharacterLimit(t *testing.T) {
	m := tuiModel{input: newTUIInput()}
	content := strings.Repeat("a", tuiInputCharLimit+10)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(content), Paste: true})
	m = updated.(tuiModel)
	if got := len([]rune(m.input.Value())); got != tuiInputCharLimit {
		t.Fatalf("textarea length = %d", got)
	}
}

type staticCompletionSource []completion.Item

func (s staticCompletionSource) Complete(context.Context, completion.Request) []completion.Item {
	return s
}

func mustWriteFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
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
