package local

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMouseWheelScrollsRegionUnderPointer(t *testing.T) {
	m := newCopyTestModel()
	m.content = []*tuiContentBlock{{kind: tuiContentUser, text: strings.Repeat("line\n", 50)}}
	m.notices = []tuiNotice{{Text: strings.Repeat("notice\n", 80)}}
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
	if region := m.mouseRegion(chatWidth+noticePanelFrameWidth, 2); region != regionNotice {
		t.Fatalf("notice mouse region = %v", region)
	}
	noticeBefore = m.noticeViewport.YOffset
	chatBefore = m.viewport.YOffset
	updated, _ = m.Update(tea.MouseMsg{X: chatWidth + noticePanelFrameWidth, Y: 2, Type: tea.MouseWheelUp, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = updated.(tuiModel)
	if m.noticeViewport.YOffset >= noticeBefore || m.viewport.YOffset != chatBefore {
		t.Fatalf("notice wheel offsets chat=%d notice=%d", m.viewport.YOffset, m.noticeViewport.YOffset)
	}
}

func TestMouseDragDividerResizesPanelsWithoutEnteringCopyMode(t *testing.T) {
	m := newCopyTestModel()
	chatWidth, noticeWidth := m.layoutWidths()
	if chatWidth != 78 || noticeWidth != noticePanelWidth {
		t.Fatalf("initial widths chat=%d notice=%d", chatWidth, noticeWidth)
	}

	updated, _ := m.Update(tea.MouseMsg{X: chatWidth, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = updated.(tuiModel)
	if !m.resizingNotice || m.copyState.active() {
		t.Fatalf("divider press resizing=%v copy=%#v", m.resizingNotice, m.copyState)
	}

	updated, _ = m.Update(tea.MouseMsg{X: 90, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	m = updated.(tuiModel)
	chatWidth, noticeWidth = m.layoutWidths()
	if chatWidth != 90 || noticeWidth != 28 || m.viewport.Width != 90 || m.noticeViewport.Width != 28 {
		t.Fatalf("drag widths chat=%d notice=%d viewports=%d/%d", chatWidth, noticeWidth, m.viewport.Width, m.noticeViewport.Width)
	}

	updated, _ = m.Update(tea.MouseMsg{X: 92, Y: 2, Action: tea.MouseActionRelease})
	m = updated.(tuiModel)
	chatWidth, noticeWidth = m.layoutWidths()
	if m.resizingNotice || chatWidth != 92 || noticeWidth != 26 {
		t.Fatalf("release resizing=%v widths=%d/%d", m.resizingNotice, chatWidth, noticeWidth)
	}

	chatMouseEvent(&m, tea.MouseActionPress, 2, 2)
	chatMouseEvent(&m, tea.MouseActionRelease, 2, 2)
	if m.copyState.active() {
		t.Fatalf("chat click entered copy mode: %#v", m.copyState)
	}
}

func TestMouseDragDividerClampsPanelWidths(t *testing.T) {
	m := newCopyTestModel()
	chatWidth, _ := m.layoutWidths()
	updated, _ := m.Update(tea.MouseMsg{X: chatWidth, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = updated.(tuiModel)

	updated, _ = m.Update(tea.MouseMsg{X: 0, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	m = updated.(tuiModel)
	chatWidth, noticeWidth := m.layoutWidths()
	if chatWidth != minChatPanelWidth || noticeWidth != 78 {
		t.Fatalf("left clamp widths chat=%d notice=%d", chatWidth, noticeWidth)
	}

	updated, _ = m.Update(tea.MouseMsg{X: m.width, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	m = updated.(tuiModel)
	chatWidth, noticeWidth = m.layoutWidths()
	if chatWidth != 98 || noticeWidth != minNoticePanelWidth {
		t.Fatalf("right clamp widths chat=%d notice=%d", chatWidth, noticeWidth)
	}
}

func TestNoticeWidthRestoredAfterNarrowWindow(t *testing.T) {
	m := newCopyTestModel()
	chatWidth, _ := m.layoutWidths()
	updated, _ := m.Update(tea.MouseMsg{X: chatWidth, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.MouseMsg{X: 58, Y: 2, Action: tea.MouseActionRelease})
	m = updated.(tuiModel)

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = updated.(tuiModel)
	chatWidth, noticeWidth := m.layoutWidths()
	if chatWidth != 80 || noticeWidth != 0 {
		t.Fatalf("narrow widths chat=%d notice=%d", chatWidth, noticeWidth)
	}

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	m = updated.(tuiModel)
	chatWidth, noticeWidth = m.layoutWidths()
	if chatWidth != 58 || noticeWidth != 60 {
		t.Fatalf("restored widths chat=%d notice=%d", chatWidth, noticeWidth)
	}
}

func chatMouseEvent(m *tuiModel, action tea.MouseAction, x, y int) {
	button := tea.MouseButtonLeft
	if action == tea.MouseActionRelease {
		button = tea.MouseButtonNone
	}
	updated, _ := m.Update(tea.MouseMsg{Button: button, Action: action, X: x, Y: y})
	*m = updated.(tuiModel)
}

func TestChatDragFromToolTitleSelectsWithoutToggling(t *testing.T) {
	m := newCopyTestModel()
	card := &tuiContentBlock{kind: tuiContentTool, toolStatus: "完成"}
	card.toolCall.Name = "shell"
	m.content = []*tuiContentBlock{card}
	m.refreshContent()
	chatMouseEvent(&m, tea.MouseActionPress, 2, 2)
	chatMouseEvent(&m, tea.MouseActionMotion, 2, 2)
	if card.toolExpanded || m.copyState.active() {
		t.Fatal("stationary press should not act before release")
	}
	chatMouseEvent(&m, tea.MouseActionMotion, 5, 2)
	chatMouseEvent(&m, tea.MouseActionRelease, 6, 2)
	if got := m.selectedCopyText(); got != "shell" || card.toolExpanded || !m.copyState.visual() {
		t.Fatalf("selection=%q expanded=%v mode=%v", got, card.toolExpanded, m.copyState.Mode)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if got := m.clipboard.(*fakeClipboard).text; got != "shell" {
		t.Fatalf("copied %q", got)
	}
}

func TestViewportUpdatesFollowOnlyWithinOneRow(t *testing.T) {
	for _, gaps := range [][2]int{{0, 2}, {2, 1}, {1, 0}} {
		m := newCopyTestModel()
		m.appendAssistantContent(strings.Repeat("chat\n", 50))
		m.appendNoticeLevel(strings.Repeat("notice\n", 50), 0)
		m.viewport.ScrollUp(gaps[0])
		m.noticeViewport.ScrollUp(gaps[1])
		chat, notice := m.viewport.YOffset, m.noticeViewport.YOffset
		m.appendAssistantContent("new chat")
		m.appendNoticeLevel("new notice", 0)
		if m.viewport.AtBottom() != (gaps[0] <= 1) || (gaps[0] > 1 && m.viewport.YOffset != chat) {
			t.Fatalf("chat gap=%d offset %d -> %d", gaps[0], chat, m.viewport.YOffset)
		}
		if m.noticeViewport.AtBottom() != (gaps[1] <= 1) || (gaps[1] > 1 && m.noticeViewport.YOffset != notice) {
			t.Fatalf("notice gap=%d offset %d -> %d", gaps[1], notice, m.noticeViewport.YOffset)
		}
	}
}
