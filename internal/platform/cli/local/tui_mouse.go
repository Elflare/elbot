// tui_mouse.go 处理终端界面的鼠标交互。

package local

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
	if event.Action == tea.MouseActionPress && event.Button == tea.MouseButtonLeft {
		switch {
		case m.isNoticeDivider(event.X, event.Y):
			m.resizingNotice = true
		case region == regionChat:
			m.chatPress, m.chatDragging = &event, false
		case region == regionNotice:
			m.enterCopyMode(region)
			m.copyState.Cursor = m.copyCursorFromMouse(region, event.X, event.Y, m.noticeCopyLines())
			m.ensureCopyCursorVisible()
		}
		return m, nil
	}
	press := m.chatPress
	if press == nil || (event.Action != tea.MouseActionMotion && event.Action != tea.MouseActionRelease) {
		return m, nil
	}
	if m.chatDragging || (len(m.content) > 0 && (event.X != press.X || event.Y != press.Y)) {
		lines := m.copyLines(regionChat)
		if !m.chatDragging {
			m.chatDragging = true
			if m.copyState.Region == regionNotice {
				m.exitCopyMode()
			}
			m.copyState = copyModeState{Mode: modeCopyVisualChar, Region: regionChat,
				Anchor: m.copyCursorFromMouse(regionChat, press.X, press.Y, lines), Status: modeName(modeCopyVisualChar)}
			m.input.Blur()
		}
		y := clampInt(event.Y, 1, m.viewport.Height)
		m.copyState.Cursor = m.copyCursorFromMouse(regionChat, event.X, y, lines)
		m.viewport.SetContent(m.renderCopyLines(regionChat, lines))
	}
	if event.Action == tea.MouseActionRelease {
		if !m.chatDragging && region == regionChat {
			m.toggleToolAt(event.X, event.Y)
		}
		m.chatPress = nil
	}
	return m, nil
}

func (m *tuiModel) toggleToolAt(x, y int) {
	line := m.viewport.YOffset + y - 1
	for _, block := range m.content {
		if block.kind != tuiContentTool {
			continue
		}
		footer := block.toolExpanded && line == block.toolFooterLine && x == m.viewport.Width-1
		if line != block.toolHeaderLine && !footer {
			continue
		}
		block.toolExpanded = !block.toolExpanded
		if footer && block.toolHeaderLine < m.viewport.YOffset {
			m.viewport.SetYOffset(block.toolHeaderLine)
		}
		if m.copyState.active() && m.copyState.Region == regionChat {
			m.exitCopyMode()
		} else {
			m.refreshContent()
		}
		return
	}
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

func (m tuiModel) copyCursorFromMouse(region tuiRegion, x, y int, lines []string) copyCursor {
	if len(lines) == 0 {
		return copyCursor{}
	}
	offset := m.viewport.YOffset
	if region == regionNotice {
		chatWidth, _ := m.layoutWidths()
		x = max(0, x-chatWidth-noticePanelFrameWidth)
		offset = m.noticeViewport.YOffset
	}
	line := clampInt(offset+y-1, 0, len(lines)-1)
	return copyCursor{Line: line, Col: colFromDisplayWidth(lines[line], x)}
}
