package cli

import (
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

type tuiMode int

const (
	modeInput tuiMode = iota
	modeCopyNormal
	modeCopyVisualChar
	modeCopyVisualLine
	modeCopySearch
)

type tuiRegion int

const (
	regionNone tuiRegion = iota
	regionChat
	regionNotice
	regionInput
	regionCompletion
)

type copyCursor struct {
	Line int
	Col  int
}

type copyModeState struct {
	Mode        tuiMode
	Region      tuiRegion
	Cursor      copyCursor
	Anchor      copyCursor
	SearchInput string
	SearchQuery string
	Status      string
}

func (s copyModeState) active() bool {
	return s.Mode != modeInput
}

func (s copyModeState) visual() bool {
	return s.Mode == modeCopyVisualChar || s.Mode == modeCopyVisualLine
}

func (m tuiModel) updateCopyMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.copyState.Mode == modeCopySearch {
		return m.updateCopySearch(msg)
	}
	switch msg.String() {
	case "alt+h":
		if m.copyState.Mode == modeCopyNormal {
			m.enterCopyMode(regionChat)
		}
		return m, nil
	case "alt+l":
		if m.copyState.Mode == modeCopyNormal {
			if m.layoutNoticeVisible() {
				m.enterCopyMode(regionNotice)
			} else {
				m.copyState.Status = "notices hidden"
			}
		}
		return m, nil
	case "esc":
		if m.copyState.visual() {
			m.cancelCopySubmode()
			return m, nil
		}
		m.exitCopyMode()
		return m, nil
	case "i":
		m.exitCopyMode()
		return m, nil
	case "h", "left":
		m.moveCopyCursor(-1, 0)
		return m, nil
	case "l", "right":
		m.moveCopyCursor(1, 0)
		return m, nil
	case "j", "down":
		m.moveCopyCursor(0, 1)
		return m, nil
	case "k", "up":
		m.moveCopyCursor(0, -1)
		return m, nil
	case "pgdown", "ctrl+d":
		m.pageCopy(1)
		return m, nil
	case "pgup", "ctrl+u":
		m.pageCopy(-1)
		return m, nil
	case "home", "g":
		m.copyState.Cursor = copyCursor{}
		m.ensureCopyCursorVisible()
		m.refreshCopyRegion()
		return m, nil
	case "end", "G":
		lines := m.copyLines(m.copyState.Region)
		if len(lines) > 0 {
			m.copyState.Cursor = copyCursor{Line: len(lines) - 1}
			m.ensureCopyCursorVisible()
			m.refreshCopyRegion()
		}
		return m, nil
	case "v":
		m.startVisual(modeCopyVisualChar)
		return m, nil
	case "V":
		m.startVisual(modeCopyVisualLine)
		return m, nil
	case "y":
		m.yankCopySelection()
		return m, nil
	case "/":
		m.beginCopySearch()
		return m, nil
	case "n":
		if !m.jumpCopySearch(1) {
			m.copyState.Status = "not found"
		}
		return m, nil
	case "N":
		if !m.jumpCopySearch(-1) {
			m.copyState.Status = "not found"
		}
		return m, nil
	}
	return m, nil
}

func (m tuiModel) updateCopySearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Alt {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.cancelCopySubmode()
		return m, nil
	case "enter":
		m.commitCopySearch()
		return m, nil
	case "backspace", "ctrl+h":
		runes := []rune(m.copyState.SearchInput)
		if len(runes) > 0 {
			m.copyState.SearchInput = string(runes[:len(runes)-1])
		}
		return m, nil
	}
	if msg.Type == tea.KeyRunes {
		m.copyState.SearchInput += string(msg.Runes)
	}
	return m, nil
}

func (m tuiModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	event := tea.MouseEvent(msg)
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
		colX = max(0, x-chatWidth-2)
	}
	line = clampInt(line, 0, len(lines)-1)
	return copyCursor{Line: line, Col: colFromDisplayWidth(lines[line], colX)}
}

func (m tuiModel) copyStatusText() string {
	if m.copyState.Mode == modeCopySearch {
		return "SEARCH " + regionName(m.copyState.Region) + " /" + m.copyState.SearchInput + " · Enter jump · Esc cancel"
	}
	status := strings.ToUpper(modeName(m.copyState.Mode)) + " " + regionName(m.copyState.Region) + " · hjkl move · v/V select · y copy · / search · i input · Esc"
	if m.copyState.Status != "" {
		status += " · " + m.copyState.Status
	}
	return status
}

func (m *tuiModel) enterCopyMode(region tuiRegion) {
	lines := m.copyLines(region)
	if len(lines) == 0 {
		m.copyState.Status = "nothing to copy"
		return
	}
	m.copyState = copyModeState{Mode: modeCopyNormal, Region: region, Cursor: copyCursor{Line: len(lines) - 1}, Status: "copy " + regionName(region)}
	m.input.Blur()
	m.refreshCopyRegion()
	m.ensureCopyCursorVisible()
	m.refreshCopyRegion()
}

func (m *tuiModel) exitCopyMode() {
	region := m.copyState.Region
	m.copyState = copyModeState{}
	m.input.Focus()
	switch region {
	case regionChat:
		m.refreshContent()
	case regionNotice:
		m.refreshNotices()
	}
}

func (m *tuiModel) copyLines(region tuiRegion) []string {
	switch region {
	case regionChat:
		return splitLines(m.wrappedContent())
	case regionNotice:
		return m.noticeCopyLines()
	default:
		return nil
	}
}

func (m tuiModel) noticeCopyLines() []string {
	if len(m.notices) == 0 {
		return nil
	}
	width := max(1, m.noticeViewport.Width-4)
	lines := []string{}
	for i, notice := range m.notices {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, splitLines(wrapDisplayWidth(notice, width))...)
	}
	return lines
}

func splitLines(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func (m *tuiModel) moveCopyCursor(dx, dy int) {
	lines := m.copyLines(m.copyState.Region)
	if len(lines) == 0 {
		m.copyState.Cursor = copyCursor{}
		return
	}
	cursor := m.copyState.Cursor
	cursor.Line = clampInt(cursor.Line+dy, 0, len(lines)-1)
	lineLen := runeLen(lines[cursor.Line])
	cursor.Col = clampInt(cursor.Col+dx, 0, max(0, lineLen-1))
	m.copyState.Cursor = cursor
	m.ensureCopyCursorVisible()
	m.refreshCopyRegion()
}

func (m *tuiModel) ensureCopyCursorVisible() {
	switch m.copyState.Region {
	case regionChat:
		ensureViewportLineVisible(&m.viewport, m.copyState.Cursor.Line)
	case regionNotice:
		ensureViewportLineVisible(&m.noticeViewport, m.copyState.Cursor.Line)
	}
}

func ensureViewportLineVisible(v *viewport.Model, line int) {
	if v == nil || v.Height <= 0 {
		return
	}
	if line < v.YOffset {
		v.YOffset = max(0, line)
		return
	}
	bottom := v.YOffset + max(1, v.Height) - 1
	if line > bottom {
		v.YOffset = max(0, line-max(1, v.Height)+1)
	}
}

func (m *tuiModel) pageCopy(delta int) {
	height := m.copyRegionHeight()
	if height <= 0 {
		height = 1
	}
	m.moveCopyCursor(0, delta*height)
}

func (m tuiModel) copyRegionHeight() int {
	switch m.copyState.Region {
	case regionChat:
		return max(1, m.viewport.Height)
	case regionNotice:
		return max(1, m.noticeViewport.Height)
	default:
		return 1
	}
}

func (m *tuiModel) startVisual(mode tuiMode) {
	m.copyState.Anchor = m.copyState.Cursor
	m.copyState.Mode = mode
	m.copyState.Status = modeName(mode)
	m.refreshCopyRegion()
}

func (m *tuiModel) cancelCopySubmode() {
	m.copyState.Mode = modeCopyNormal
	m.copyState.Anchor = copyCursor{}
	m.copyState.SearchInput = ""
	m.copyState.Status = "copy " + regionName(m.copyState.Region)
	m.refreshCopyRegion()
}

func (m *tuiModel) yankCopySelection() {
	text := m.selectedCopyText()
	if text == "" {
		m.copyState.Status = "nothing copied"
		return
	}
	if err := m.clipboard.WriteAll(text); err != nil {
		m.copyState.Status = "copy failed: " + err.Error()
		return
	}
	lineCount := strings.Count(text, "\n") + 1
	m.copyState.Status = "copied " + plural(lineCount, "line")
	m.copyState.Mode = modeCopyNormal
	m.copyState.Anchor = copyCursor{}
	m.refreshCopyRegion()
}

func (m tuiModel) selectedCopyText() string {
	lines := m.copyLines(m.copyState.Region)
	if len(lines) == 0 {
		return ""
	}
	cursor := clampCursor(m.copyState.Cursor, lines)
	if !m.copyState.visual() {
		return lines[cursor.Line]
	}
	anchor := clampCursor(m.copyState.Anchor, lines)
	if m.copyState.Mode == modeCopyVisualLine {
		start, end := orderedLines(anchor.Line, cursor.Line)
		return strings.Join(lines[start:end+1], "\n")
	}
	start, end := orderedCursors(anchor, cursor)
	if start.Line == end.Line {
		return sliceRunesInclusive(lines[start.Line], start.Col, end.Col)
	}
	parts := []string{sliceRunesFrom(lines[start.Line], start.Col)}
	for line := start.Line + 1; line < end.Line; line++ {
		parts = append(parts, lines[line])
	}
	parts = append(parts, sliceRunesTo(lines[end.Line], end.Col))
	return strings.Join(parts, "\n")
}

func (m *tuiModel) beginCopySearch() {
	m.copyState.Mode = modeCopySearch
	m.copyState.SearchInput = ""
	m.copyState.Status = "search"
}

func (m *tuiModel) commitCopySearch() {
	query := strings.TrimSpace(m.copyState.SearchInput)
	m.copyState.Mode = modeCopyNormal
	m.copyState.SearchInput = ""
	if query == "" {
		m.copyState.Status = "empty search"
		return
	}
	m.copyState.SearchQuery = query
	if !m.jumpCopySearch(1) {
		m.copyState.Status = "not found: " + query
	}
}

func (m *tuiModel) jumpCopySearch(direction int) bool {
	query := m.copyState.SearchQuery
	if query == "" {
		return false
	}
	lines := m.copyLines(m.copyState.Region)
	if len(lines) == 0 {
		return false
	}
	start := m.copyState.Cursor.Line
	for step := 1; step <= len(lines); step++ {
		idx := (start + direction*step + len(lines)*step) % len(lines)
		col := strings.Index(strings.ToLower(lines[idx]), strings.ToLower(query))
		if col >= 0 {
			m.copyState.Cursor = copyCursor{Line: idx, Col: runeLen(lines[idx][:col])}
			m.copyState.Status = "found: " + query
			m.ensureCopyCursorVisible()
			m.refreshCopyRegion()
			return true
		}
	}
	return false
}

func (m tuiModel) renderCopyLines(region tuiRegion, lines []string) string {
	if !m.copyState.active() || m.copyState.Region != region {
		return strings.Join(lines, "\n")
	}
	rendered := append([]string(nil), lines...)
	for i, line := range rendered {
		rendered[i] = m.renderCopyLine(i, line)
	}
	return strings.Join(rendered, "\n")
}

func (m tuiModel) renderCopyLine(lineIndex int, line string) string {
	if m.copyState.Mode == modeCopyVisualLine && lineInRange(lineIndex, m.copyState.Anchor.Line, m.copyState.Cursor.Line) {
		return tuiCopySelectionStyle.Render(line)
	}
	if m.copyState.Mode == modeCopyVisualChar && lineInVisualCharRange(lineIndex, m.copyState.Anchor, m.copyState.Cursor) {
		start, end := selectionColsForLine(lineIndex, m.copyState.Anchor, m.copyState.Cursor)
		return renderCharSelection(line, start, end)
	}
	if matches := searchMatchRanges(line, m.copyState.SearchQuery); len(matches) > 0 {
		current := -1
		if lineIndex == m.copyState.Cursor.Line {
			for i, match := range matches {
				if match.start == m.copyState.Cursor.Col {
					current = i
					break
				}
			}
		}
		return renderSearchMatches(line, matches, current)
	}
	if lineIndex == m.copyState.Cursor.Line {
		return renderCursor(line, m.copyState.Cursor.Col)
	}
	return line
}

type copyRange struct {
	start int
	end   int
}

func searchMatchRanges(line, query string) []copyRange {
	if query == "" || line == "" {
		return nil
	}
	lowerLine := strings.ToLower(line)
	lowerQuery := strings.ToLower(query)
	queryLen := runeLen(query)
	if queryLen == 0 {
		return nil
	}
	matches := []copyRange{}
	byteOffset := 0
	runeOffset := 0
	for byteOffset <= len(lowerLine) {
		idx := strings.Index(lowerLine[byteOffset:], lowerQuery)
		if idx < 0 {
			break
		}
		prefix := lowerLine[byteOffset : byteOffset+idx]
		start := runeOffset + runeLen(prefix)
		matches = append(matches, copyRange{start: start, end: start + queryLen - 1})
		consumed := idx + len(lowerQuery)
		byteOffset += consumed
		runeOffset += runeLen(lowerLine[byteOffset-consumed : byteOffset])
	}
	return matches
}

func searchMatchRange(line, query string, cursor copyCursor) (int, int, bool) {
	if query == "" {
		return 0, 0, false
	}
	start := cursor.Col
	end := start + runeLen(query) - 1
	if start < 0 || start >= runeLen(line) || end < start {
		return 0, 0, false
	}
	return start, end, true
}

func renderSearchMatches(line string, matches []copyRange, current int) string {
	runes := []rune(line)
	if len(runes) == 0 || len(matches) == 0 {
		return line
	}
	var sb strings.Builder
	pos := 0
	for i, match := range matches {
		start := clampInt(match.start, pos, len(runes))
		end := clampInt(match.end, start-1, len(runes)-1)
		if start > pos {
			sb.WriteString(string(runes[pos:start]))
		}
		if end >= start {
			style := tuiCopySearchStyle
			if i == current {
				style = tuiCopySearchCurrentStyle
			}
			sb.WriteString(style.Render(string(runes[start : end+1])))
			pos = end + 1
		}
	}
	if pos < len(runes) {
		sb.WriteString(string(runes[pos:]))
	}
	return sb.String()
}

func renderCursor(line string, col int) string {
	runes := []rune(line)
	if len(runes) == 0 {
		return tuiCopyCursorStyle.Render(" ")
	}
	col = clampInt(col, 0, len(runes)-1)
	return string(runes[:col]) + tuiCopyCursorStyle.Render(string(runes[col])) + string(runes[col+1:])
}

func renderCharSelection(line string, start, end int) string {
	runes := []rune(line)
	if len(runes) == 0 {
		return tuiCopySelectionStyle.Render(" ")
	}
	start = clampInt(start, 0, len(runes)-1)
	end = clampInt(end, start, len(runes)-1)
	return string(runes[:start]) + tuiCopySelectionStyle.Render(string(runes[start:end+1])) + string(runes[end+1:])
}

func selectionColsForLine(line int, a, b copyCursor) (int, int) {
	start, end := orderedCursors(a, b)
	if line == start.Line && line == end.Line {
		return start.Col, end.Col
	}
	if line == start.Line {
		return start.Col, 1 << 30
	}
	if line == end.Line {
		return 0, end.Col
	}
	return 0, 1 << 30
}

func lineInVisualCharRange(line int, a, b copyCursor) bool {
	start, end := orderedCursors(a, b)
	return line >= start.Line && line <= end.Line
}

func lineInRange(line, a, b int) bool {
	start, end := orderedLines(a, b)
	return line >= start && line <= end
}

func orderedLines(a, b int) (int, int) {
	if a <= b {
		return a, b
	}
	return b, a
}

func orderedCursors(a, b copyCursor) (copyCursor, copyCursor) {
	items := []copyCursor{a, b}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Line == items[j].Line {
			return items[i].Col < items[j].Col
		}
		return items[i].Line < items[j].Line
	})
	return items[0], items[1]
}

func clampCursor(cursor copyCursor, lines []string) copyCursor {
	if len(lines) == 0 {
		return copyCursor{}
	}
	cursor.Line = clampInt(cursor.Line, 0, len(lines)-1)
	cursor.Col = clampInt(cursor.Col, 0, max(0, runeLen(lines[cursor.Line])-1))
	return cursor
}

func sliceRunesInclusive(text string, start, end int) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	start = clampInt(start, 0, len(runes)-1)
	end = clampInt(end, start, len(runes)-1)
	return string(runes[start : end+1])
}

func sliceRunesFrom(text string, start int) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	start = clampInt(start, 0, len(runes)-1)
	return string(runes[start:])
}

func sliceRunesTo(text string, end int) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	end = clampInt(end, 0, len(runes)-1)
	return string(runes[:end+1])
}

func runeLen(text string) int {
	return len([]rune(text))
}

func colFromDisplayWidth(text string, width int) int {
	if width <= 0 {
		return 0
	}
	current := 0
	for i, r := range []rune(text) {
		current += runewidth.RuneWidth(r)
		if current > width {
			return i
		}
	}
	return max(0, runeLen(text)-1)
}

func regionName(region tuiRegion) string {
	switch region {
	case regionChat:
		return "chat"
	case regionNotice:
		return "notices"
	case regionInput:
		return "input"
	case regionCompletion:
		return "completion"
	default:
		return "none"
	}
}

func modeName(mode tuiMode) string {
	switch mode {
	case modeCopyNormal:
		return "copy"
	case modeCopyVisualChar:
		return "visual"
	case modeCopyVisualLine:
		return "visual line"
	case modeCopySearch:
		return "search"
	default:
		return "input"
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func clampInt(v, low, high int) int {
	if high < low {
		return low
	}
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

var (
	tuiCopyCursorStyle        = lipgloss.NewStyle().Reverse(true)
	tuiCopySelectionStyle     = lipgloss.NewStyle().Background(lipgloss.Color("62")).Foreground(lipgloss.Color("15"))
	tuiCopySearchStyle        = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("11"))
	tuiCopySearchCurrentStyle = lipgloss.NewStyle().Background(lipgloss.Color("99")).Foreground(lipgloss.Color("15"))
)
