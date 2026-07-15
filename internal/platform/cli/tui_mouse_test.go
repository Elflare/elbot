package cli

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

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

	updated, _ = m.Update(tea.MouseMsg{X: 2, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = updated.(tuiModel)
	if !m.copyState.active() || m.copyState.Region != regionChat {
		t.Fatalf("chat click copy state = %#v", m.copyState)
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
