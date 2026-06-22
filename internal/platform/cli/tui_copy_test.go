package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type fakeClipboard struct {
	text string
	err  error
}

func (c *fakeClipboard) WriteAll(text string) error {
	c.text = text
	return c.err
}

func newCopyTestModel() tuiModel {
	input := textinput.New()
	input.Focus()
	m := tuiModel{
		handler:       fakeCompletingHandler{},
		clipboard:     &fakeClipboard{},
		userName:      "user",
		assistantName: "assistant",
		input:         input,
		width:         120,
		height:        20,
		ctx:           context.Background(),
	}
	m.resizeViewports()
	m.content = "user: hello\nassistant: first line\nsecond error line\nthird line\n"
	m.notices = []string{"notice one", "notice two error"}
	m.refreshContent()
	m.refreshNotices()
	return m
}

func TestAltKeysEnterCopyModeAndEscOrIReturnsInput(t *testing.T) {
	m := newCopyTestModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}, Alt: true})
	m = updated.(tuiModel)
	if m.copyState.Mode != modeCopyNormal || m.copyState.Region != regionChat || m.input.Focused() {
		t.Fatalf("alt+h copy state = %#v focused=%v", m.copyState, m.input.Focused())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	m = updated.(tuiModel)
	if m.copyState.active() || !m.input.Focused() {
		t.Fatalf("i should return input mode, state=%#v focused=%v", m.copyState, m.input.Focused())
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}, Alt: true})
	m = updated.(tuiModel)
	if m.copyState.Mode != modeCopyNormal || m.copyState.Region != regionNotice {
		t.Fatalf("alt+l copy state = %#v", m.copyState)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(tuiModel)
	if m.copyState.active() || !m.input.Focused() {
		t.Fatalf("esc should return input mode, state=%#v focused=%v", m.copyState, m.input.Focused())
	}
}

func TestEnterCopyModeStartsAtBottom(t *testing.T) {
	m := newCopyTestModel()
	m.enterCopyMode(regionChat)
	lines := m.copyLines(regionChat)
	if m.copyState.Cursor.Line != len(lines)-1 {
		t.Fatalf("cursor line = %d, want %d", m.copyState.Cursor.Line, len(lines)-1)
	}
}

func TestAltKeysSwitchRegionFromCopyNormalOnly(t *testing.T) {
	m := newCopyTestModel()
	m.enterCopyMode(regionChat)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}, Alt: true})
	m = updated.(tuiModel)
	if m.copyState.Region != regionNotice || m.copyState.Mode != modeCopyNormal {
		t.Fatalf("alt+l in copy normal state = %#v", m.copyState)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}, Alt: true})
	m = updated.(tuiModel)
	if m.copyState.Region != regionNotice || m.copyState.Mode != modeCopySearch {
		t.Fatalf("alt+h should be ignored during search: %#v", m.copyState)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}, Alt: true})
	m = updated.(tuiModel)
	if m.copyState.Region != regionNotice || m.copyState.Mode != modeCopyVisualLine {
		t.Fatalf("alt+h should be ignored during visual: %#v", m.copyState)
	}
}

func TestSearchMatchRangesFindsAllMatches(t *testing.T) {
	matches := searchMatchRanges("error ok ERROR error", "error")
	want := []copyRange{{start: 0, end: 4}, {start: 9, end: 13}, {start: 15, end: 19}}
	if len(matches) != len(want) {
		t.Fatalf("matches = %#v", matches)
	}
	for i := range want {
		if matches[i] != want[i] {
			t.Fatalf("matches[%d] = %#v, want %#v", i, matches[i], want[i])
		}
	}
}

func TestRenderSearchLineKeepsCursorVisible(t *testing.T) {
	m := newCopyTestModel()
	m.enterCopyMode(regionChat)
	m.copyState.SearchQuery = "error"
	m.copyState.Cursor = copyCursor{Line: 2, Col: 8}

	rendered := m.renderCopyLine(2, "second error line")
	if !strings.Contains(rendered, tuiCopyCursorStyle.Render("r")) {
		t.Fatalf("rendered line should contain cursor on current character: %q", rendered)
	}
}

func TestCopyNormalYCopiesCurrentLineAndStaysInCopyMode(t *testing.T) {
	m := newCopyTestModel()
	clip := m.clipboard.(*fakeClipboard)
	m.enterCopyMode(regionChat)
	m.copyState.Cursor = copyCursor{Line: 1}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(tuiModel)
	if clip.text != "assistant: first line" {
		t.Fatalf("copied = %q", clip.text)
	}
	if m.copyState.Mode != modeCopyNormal || !m.copyState.active() {
		t.Fatalf("copy should stay in normal copy mode: %#v", m.copyState)
	}
}

func TestVisualLineYCopiesRangeAndReturnsCopyNormal(t *testing.T) {
	m := newCopyTestModel()
	clip := m.clipboard.(*fakeClipboard)
	m.enterCopyMode(regionChat)
	m.copyState.Cursor = copyCursor{Line: 1}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(tuiModel)

	want := "assistant: first line\nsecond error line"
	if clip.text != want {
		t.Fatalf("copied = %q, want %q", clip.text, want)
	}
	if m.copyState.Mode != modeCopyNormal || !m.copyState.active() {
		t.Fatalf("after y state = %#v", m.copyState)
	}
}

func TestVisualCharYCopiesRange(t *testing.T) {
	m := newCopyTestModel()
	clip := m.clipboard.(*fakeClipboard)
	m.enterCopyMode(regionChat)
	m.copyState.Cursor = copyCursor{Line: 1, Col: 11}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(tuiModel)

	if clip.text != "fir" {
		t.Fatalf("copied = %q", clip.text)
	}
	if m.copyState.Mode != modeCopyNormal {
		t.Fatalf("after y mode = %v", m.copyState.Mode)
	}
}

func TestCopySearchJumpsAndRepeats(t *testing.T) {
	m := newCopyTestModel()
	m.enterCopyMode(regionChat)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(tuiModel)
	for _, r := range "error" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(tuiModel)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.copyState.Mode != modeCopyNormal || m.copyState.Cursor.Line != 2 {
		t.Fatalf("search state = %#v", m.copyState)
	}
}

func TestCopyWordMotionsMoveWithinLine(t *testing.T) {
	m := newCopyTestModel()
	m.content = "alpha beta gamma\n"
	m.refreshContent()
	m.enterCopyMode(regionChat)
	m.copyState.Cursor = copyCursor{Line: 0, Col: 0}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	m = updated.(tuiModel)
	if m.copyState.Cursor != (copyCursor{Line: 0, Col: 6}) {
		t.Fatalf("w cursor = %#v", m.copyState.Cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(tuiModel)
	if m.copyState.Cursor != (copyCursor{Line: 0, Col: 9}) {
		t.Fatalf("e cursor = %#v", m.copyState.Cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	m = updated.(tuiModel)
	if m.copyState.Cursor != (copyCursor{Line: 0, Col: 6}) {
		t.Fatalf("b cursor = %#v", m.copyState.Cursor)
	}
}

func TestCopyWordMotionsCrossLinesAndUnicode(t *testing.T) {
	m := newCopyTestModel()
	m.content = "alpha beta\n\n助手 冰火_test 42!\n"
	m.refreshContent()
	m.enterCopyMode(regionChat)
	m.copyState.Cursor = copyCursor{Line: 0, Col: 6}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	m = updated.(tuiModel)
	if m.copyState.Cursor != (copyCursor{Line: 2, Col: 0}) {
		t.Fatalf("cross-line w cursor = %#v", m.copyState.Cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	m = updated.(tuiModel)
	if m.copyState.Cursor != (copyCursor{Line: 2, Col: 3}) {
		t.Fatalf("unicode w cursor = %#v", m.copyState.Cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(tuiModel)
	if m.copyState.Cursor != (copyCursor{Line: 2, Col: 9}) {
		t.Fatalf("unicode e cursor = %#v", m.copyState.Cursor)
	}
}

func TestVisualCharWordMotionExtendsSelection(t *testing.T) {
	m := newCopyTestModel()
	clip := m.clipboard.(*fakeClipboard)
	m.content = "alpha beta gamma\n"
	m.refreshContent()
	m.enterCopyMode(regionChat)
	m.copyState.Cursor = copyCursor{Line: 0, Col: 0}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(tuiModel)

	if clip.text != "alpha" {
		t.Fatalf("copied = %q", clip.text)
	}
	if m.copyState.Mode != modeCopyNormal {
		t.Fatalf("after y mode = %v", m.copyState.Mode)
	}
}

func TestMouseWheelScrollsRegionUnderPointer(t *testing.T) {
	m := newCopyTestModel()
	m.content = ""
	for i := 0; i < 50; i++ {
		m.content += "line\n"
	}
	m.notices = []string{strings.Repeat("notice\n", 80)}
	m.refreshContent()
	m.refreshNotices()
	chatBefore := m.viewport.YOffset
	noticeBefore := m.noticeViewport.YOffset

	updated, _ := m.Update(tea.MouseMsg{X: 2, Y: 2, Type: tea.MouseWheelUp, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = updated.(tuiModel)
	if m.viewport.YOffset >= chatBefore || m.noticeViewport.YOffset != noticeBefore {
		t.Fatalf("chat wheel offsets chat=%d notice=%d", m.viewport.YOffset, m.noticeViewport.YOffset)
	}

	chatWidth, _ := m.layoutWidths()
	if region := m.mouseRegion(chatWidth+2, 2); region != regionNotice {
		t.Fatalf("notice mouse region = %v", region)
	}
	noticeBefore = m.noticeViewport.YOffset
	chatBefore = m.viewport.YOffset
	updated, _ = m.Update(tea.MouseMsg{X: chatWidth + 2, Y: 2, Type: tea.MouseWheelUp, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = updated.(tuiModel)
	if m.noticeViewport.YOffset >= noticeBefore || m.viewport.YOffset != chatBefore {
		t.Fatalf("notice wheel offsets chat=%d notice=%d", m.viewport.YOffset, m.noticeViewport.YOffset)
	}
}
