package cli

import tea "github.com/charmbracelet/bubbletea"

func (m tuiModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	event := tea.MouseEvent(msg)
	if m.resizingNotice {
		switch event.Action {
		case tea.MouseActionMotion:
			m.resizeNoticeFromDivider(event.X)
			return m, nil
		case tea.MouseActionRelease:
			m.resizeNoticeFromDivider(event.X)
			m.resizingNotice = false
			return m, nil
		}
	}
	if event.Action == tea.MouseActionPress && event.Button == tea.MouseButtonLeft && m.isNoticeDivider(event.X, event.Y) {
		m.resizingNotice = true
		return m, nil
	}

	region := m.mouseRegion(event.X, event.Y)
	if event.IsWheel() || event.Type == tea.MouseWheelUp || event.Type == tea.MouseWheelDown {
		scrollUp := event.Button == tea.MouseButtonWheelUp || event.Type == tea.MouseWheelUp
		scrollDown := event.Button == tea.MouseButtonWheelDown || event.Type == tea.MouseWheelDown
		switch region {
		case regionNotice:
			delta := mouseWheelDelta(m.noticeViewport.MouseWheelDelta)
			if scrollUp {
				m.noticeViewport.ScrollUp(delta)
			} else if scrollDown {
				m.noticeViewport.ScrollDown(delta)
			}
		default:
			delta := mouseWheelDelta(m.viewport.MouseWheelDelta)
			if scrollUp {
				m.viewport.ScrollUp(delta)
			} else if scrollDown {
				m.viewport.ScrollDown(delta)
			}
		}
		return m, nil
	}
	if event.Action == tea.MouseActionPress && event.Button == tea.MouseButtonLeft && (region == regionChat || region == regionNotice) {
		m.enterCopyMode(region)
		m.copyState.Cursor = m.copyCursorFromMouse(region, event.X, event.Y)
		m.ensureCopyCursorVisible()
		return m, nil
	}
	return m, nil
}

func (m tuiModel) isNoticeDivider(x, y int) bool {
	chatWidth, noticeWidth := m.layoutWidths()
	if noticeWidth <= 0 || x != chatWidth {
		return false
	}
	bodyTop := 1
	bodyBottom := bodyTop + max(1, m.viewport.Height) - 1
	return y >= bodyTop && y <= bodyBottom
}

func (m *tuiModel) resizeNoticeFromDivider(dividerX int) {
	maxNoticeWidth := m.width - noticePanelFrameWidth - minChatPanelWidth
	m.noticeWidth = clampInt(m.width-dividerX-noticePanelFrameWidth, minNoticePanelWidth, maxNoticeWidth)
	m.resizeViewports()
	m.refreshContent()
	m.refreshNotices()
}

func mouseWheelDelta(delta int) int {
	if delta <= 0 {
		return 3
	}
	return delta
}

func (m tuiModel) mouseRegion(x, y int) tuiRegion {
	if y == 0 {
		return regionNone
	}
	bodyTop := 1
	bodyBottom := bodyTop + max(1, m.viewport.Height) - 1
	if y >= bodyTop && y <= bodyBottom {
		chatWidth, noticeWidth := m.layoutWidths()
		if noticeWidth > 0 && x >= chatWidth {
			return regionNotice
		}
		return regionChat
	}
	completionTop := bodyBottom + 1
	completionBottom := completionTop + m.completionViewHeight()
	if m.completionState.visible() && y >= completionTop && y <= completionBottom {
		return regionCompletion
	}
	if y >= m.height-1 {
		return regionInput
	}
	return regionNone
}

func (m tuiModel) copyCursorFromMouse(region tuiRegion, x, y int) copyCursor {
	lines := m.copyLines(region)
	if len(lines) == 0 {
		return copyCursor{}
	}
	bodyY := y - 1
	line := bodyY
	colX := x
	if region == regionChat {
		line += m.viewport.YOffset
	} else if region == regionNotice {
		chatWidth, _ := m.layoutWidths()
		line += m.noticeViewport.YOffset
		colX = max(0, x-chatWidth-noticePanelFrameWidth)
	}
	line = clampInt(line, 0, len(lines)-1)
	return copyCursor{Line: line, Col: colFromDisplayWidth(lines[line], colX)}
}
