package cli

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"elbot/internal/completion"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
)

type tuiOutputMsg string
type tuiReplaceAssistantMsg string
type tuiFinishAssistantMsg struct{}
type tuiReasoningMsg string
type tuiStatusMsg runtimestatus.Snapshot
type tuiStatusTickMsg time.Time
type tuiNoticeMsg string

type tuiProgramSetter func(*tea.Program)

const (
	noticePanelWidth        = 40
	maxCompletionPopupItems = 8
)

var (
	tuiTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)
	tuiTitleMutedStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiStatusStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiUserStyle               = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	tuiAssistantStyle          = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("217"))
	tuiReasoningStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	tuiNoticeStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	tuiSeparatorStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiPanelStyle              = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(lipgloss.Color("8")).PaddingLeft(1)
	tuiCompletionStyle         = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(0, 1)
	tuiCompletionSelectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62"))
)

type legacyCompleter interface {
	Complete(text string) []string
}

type completionState struct {
	base  string
	items []completion.Item
	index int
}

func (s completionState) visible() bool {
	return len(s.items) > 1
}

func (s completionState) currentText() string {
	if len(s.items) == 0 || s.index < 0 || s.index >= len(s.items) {
		return ""
	}
	return s.items[s.index].Text
}

type tuiModel struct {
	ctx     context.Context
	handler platform.PlatformHandler
	output  chan tea.Msg

	copyState       copyModeState
	clipboard       clipboardWriter
	completion      *completion.Service
	completionState completionState
	content         string
	notices         []string
	history         []string
	histPos         int
	userName        string
	assistantName   string
	assistantOpen   bool
	assistantStart  int
	reasoningOpen   bool
	runtimeStatus   runtimestatus.Snapshot
	statusNow       time.Time
	viewport        viewport.Model
	noticeViewport  viewport.Model
	input           textinput.Model
	width           int
	height          int
}

func runTUI(ctx context.Context, handler platform.PlatformHandler, completion *completion.Service, output chan tea.Msg, setProgram tuiProgramSetter, userName, assistantName string) error {
	input := textinput.New()
	input.Focus()
	input.Prompt = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")).Render("❯ ")
	input.CharLimit = 4096

	if userName == "" {
		userName = "user"
	}
	if assistantName == "" {
		assistantName = "assistant"
	}

	m := tuiModel{
		ctx:           ctx,
		handler:       handler,
		output:        output,
		copyState:     copyModeState{},
		clipboard:     newClipboardWriter(),
		completion:    completion,
		input:         input,
		userName:      userName,
		assistantName: assistantName,
	}
	program := tea.NewProgram(m, tea.WithMouseCellMotion())
	if setProgram != nil {
		setProgram(program)
		defer setProgram(nil)
	}
	// 启用鼠标捕获，用于分区滚动和 copy mode 区域选择。
	_, err := program.Run()
	return err
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(waitTUIOutput(m.output), tickTUIStatus())
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeViewports()
		m.refreshContent()
		m.refreshNotices()
		return m, nil
	case tuiOutputMsg:
		m.appendAssistantContent(string(msg))
		return m, waitTUIOutput(m.output)
	case tuiReplaceAssistantMsg:
		m.replaceAssistantContent(string(msg))
		return m, waitTUIOutput(m.output)
	case tuiFinishAssistantMsg:
		m.finishAssistantContent()
		return m, waitTUIOutput(m.output)
	case tuiReasoningMsg:
		m.appendReasoningContent(string(msg))
		return m, waitTUIOutput(m.output)
	case tuiStatusMsg:
		m.runtimeStatus = runtimestatus.Snapshot(msg)
		if m.statusNow.IsZero() {
			m.statusNow = time.Now()
		}
		return m, waitTUIOutput(m.output)
	case tuiStatusTickMsg:
		m.statusNow = time.Time(msg)
		return m, tickTUIStatus()
	case tuiNoticeMsg:
		m.appendNotice(string(msg))
		return m, waitTUIOutput(m.output)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case tea.KeyMsg:
		if m.copyState.active() {
			return m.updateCopyMode(msg)
		}
		switch msg.String() {
		case "alt+h":
			m.enterCopyMode(regionChat)
			return m, nil
		case "alt+l":
			if m.layoutNoticeVisible() {
				m.enterCopyMode(regionNotice)
			} else {
				m.copyState.Status = "notices hidden"
			}
			return m, nil
		case "esc":
			if m.completionState.visible() {
				m.clearCompletion()
				return m, nil
			}
			if m.input.Value() != "" {
				m.input.SetValue("")
			}
			return m, nil
		case "ctrl+c":
			if m.completionState.visible() {
				m.clearCompletion()
				return m, nil
			}
			if m.input.Value() != "" {
				m.input.SetValue("")
				return m, nil
			}
			return m, tea.Quit
		case "pgup":
			m.viewport.ScrollUp(max(1, m.viewport.Height-1))
			return m, nil
		case "pgdown":
			m.viewport.ScrollDown(max(1, m.viewport.Height-1))
			return m, nil
		case "ctrl+k":
			m.viewport.ScrollUp(max(1, m.viewport.Height/4))
			return m, nil
		case "ctrl+j":
			m.viewport.ScrollDown(max(1, m.viewport.Height/4))
			return m, nil
		case "ctrl+u":
			m.noticeViewport.ScrollUp(max(1, m.noticeViewport.Height/2))
			return m, nil
		case "ctrl+d":
			m.noticeViewport.ScrollDown(max(1, m.noticeViewport.Height/2))
			return m, nil
		case "home":
			m.viewport.GotoTop()
			return m, nil
		case "end":
			m.viewport.GotoBottom()
			return m, nil
		case "tab", "ctrl+i":
			return m.completeInput(1)
		case "shift+tab", "backtab":
			return m.completeInput(-1)
		case "up":
			if m.completionState.visible() {
				m.selectCompletion(-1)
				return m, nil
			}
			m.clearCompletion()
			return m.prevHistory()
		case "down":
			if m.completionState.visible() {
				m.selectCompletion(1)
				return m, nil
			}
			m.clearCompletion()
			return m.nextHistory()
		case "enter":
			m.clearCompletion()
			text := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			if text == "" {
				return m, nil
			}
			if text == "/exit" {
				return m, tea.Quit
			}
			m.history = append(m.history, text)
			m.histPos = len(m.history)
			m.appendUserContent(text)
			go func() {
				if err := m.handler.HandleMessage(m.ctx, text); err != nil {
					select {
					case m.output <- tuiNoticeMsg("error: " + err.Error() + "\n"):
					case <-m.ctx.Done():
					}
				}
			}()
			return m, nil
		}
	}

	if m.copyState.active() {
		return m, nil
	}
	oldInput := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != oldInput {
		m.clearCompletion()
	}
	return m, cmd
}

func (m tuiModel) View() string {
	return m.headerView() + "\n" + m.bodyView() + m.completionView() + "\n" + m.statusView() + "\n" + m.input.View()
}

func (m *tuiModel) resizeViewports() {
	chatWidth, noticeWidth := m.layoutWidths()
	bodyHeight := max(1, m.height-4-m.completionViewHeight())
	m.viewport.Width = chatWidth
	m.viewport.Height = bodyHeight
	m.noticeViewport.Width = noticeWidth
	m.noticeViewport.Height = bodyHeight
	m.input.Width = m.width
}

func (m tuiModel) completionViewHeight() int {
	if !m.completionState.visible() {
		return 0
	}
	count := min(len(m.completionState.items), maxCompletionPopupItems)
	return count + 3
}

func (m tuiModel) completionView() string {
	if !m.completionState.visible() {
		return ""
	}
	items := m.completionState.items
	start := 0
	if m.completionState.index >= maxCompletionPopupItems {
		start = m.completionState.index - maxCompletionPopupItems + 1
	}
	end := min(len(items), start+maxCompletionPopupItems)
	lines := make([]string, 0, end-start+1)
	for i := start; i < end; i++ {
		text := completionDisplayText(items[i])
		if i == m.completionState.index {
			text = tuiCompletionSelectedStyle.Width(max(1, m.completionPopupWidth()-4)).Render(text)
		}
		lines = append(lines, text)
	}
	lines = append(lines, tuiTitleMutedStyle.Render(strconv.Itoa(m.completionState.index+1)+"/"+strconv.Itoa(len(items))+" · Tab/↑/↓"))
	return "\n" + tuiCompletionStyle.Width(m.completionPopupWidth()).Render(strings.Join(lines, "\n"))
}

func (m tuiModel) completionPopupWidth() int {
	chatWidth, _ := m.layoutWidths()
	return min(max(24, chatWidth/2), max(24, chatWidth))
}

func completionDisplayText(item completion.Item) string {
	label := item.Label
	if label == "" {
		label = item.Text
	}
	if item.Description == "" {
		return label
	}
	return label + "  " + item.Description
}

func (m tuiModel) headerView() string {
	chatWidth, noticeWidth := m.layoutWidths()
	if m.width <= 0 {
		return tuiTitleStyle.Render("ElBot CLI")
	}
	chatTitle := tuiTitleStyle.Render("ElBot CLI")
	if noticeWidth > 0 {
		chatTitle += " " + tuiTitleMutedStyle.Render("chat")
	}
	header := lipgloss.NewStyle().Width(chatWidth).Render(chatTitle)
	if noticeWidth <= 0 {
		return header
	}
	noticeTitle := tuiTitleMutedStyle.Width(noticeWidth).Render("notices")
	return lipgloss.JoinHorizontal(lipgloss.Top, header, noticeTitle)
}

func (m tuiModel) statusView() string {
	if m.copyState.active() {
		return tuiStatusStyle.Render(m.copyStatusText())
	}
	status := m.runtimeStatusText()
	keys := "Alt+h copy · Ctrl+C exit"
	if status == "" {
		keys = "Alt+h chat copy · Alt+l notices copy · Esc clear · Ctrl+C exit · C-k/C-j chat · C-u/C-d notices"
	} else {
		status += "     "
	}
	if m.completionState.visible() {
		keys += " · completion " + strconv.Itoa(m.completionState.index+1) + "/" + strconv.Itoa(len(m.completionState.items))
	}
	return tuiStatusStyle.Render(status + keys)
}

func (m tuiModel) bodyView() string {
	chatWidth, noticeWidth := m.layoutWidths()
	if noticeWidth <= 0 {
		return m.viewport.View()
	}
	chat := lipgloss.NewStyle().Width(chatWidth).Render(m.viewport.View())
	notice := tuiPanelStyle.Width(noticeWidth).Render(m.noticeViewport.View())
	return lipgloss.JoinHorizontal(lipgloss.Top, chat, notice)
}

func (m tuiModel) layoutWidths() (int, int) {
	if m.width < 100 {
		return max(1, m.width), 0
	}
	noticeWidth := noticePanelWidth
	chatWidth := max(1, m.width-noticeWidth-2)
	return chatWidth, noticeWidth
}

func (m tuiModel) completeInput(delta int) (tea.Model, tea.Cmd) {
	if m.completionState.visible() {
		m.selectCompletion(delta)
		return m, nil
	}
	items := m.complete(m.input.Value())
	switch len(items) {
	case 0:
		m.clearCompletion()
		return m, nil
	case 1:
		m.clearCompletion()
		m.applyCompletionItem(items[0], m.input.Value())
	default:
		index := 0
		if delta < 0 {
			index = len(items) - 1
		}
		m.completionState = completionState{base: m.input.Value(), items: append([]completion.Item(nil), items...), index: index}
		m.applyCurrentCompletion()
		m.resizeViewports()
	}
	return m, nil
}

func (m tuiModel) complete(value string) []completion.Item {
	if m.completion != nil {
		return m.completion.Complete(m.ctx, completion.Request{Text: value, Cursor: m.input.Position()})
	}

	c, ok := m.handler.(legacyCompleter)
	if !ok {
		return nil
	}
	texts := c.Complete(value)
	items := make([]completion.Item, 0, len(texts))
	for _, text := range texts {
		items = append(items, completion.Item{Text: text})
	}
	return items
}

func (m *tuiModel) clearCompletion() {
	m.completionState = completionState{}
	m.resizeViewports()
}

func (m *tuiModel) selectCompletion(delta int) {
	if !m.completionState.visible() {
		return
	}
	count := len(m.completionState.items)
	m.completionState.index = (m.completionState.index + delta + count) % count
	m.applyCurrentCompletion()
}

func (m *tuiModel) applyCurrentCompletion() {
	if len(m.completionState.items) == 0 || m.completionState.index < 0 || m.completionState.index >= len(m.completionState.items) {
		return
	}
	m.applyCompletionItem(m.completionState.items[m.completionState.index], m.completionState.base)
}

func (m *tuiModel) applyCompletionItem(item completion.Item, value string) {
	text := item.Text
	if text == "" {
		return
	}
	if item.ReplaceStart >= 0 && item.ReplaceEnd >= item.ReplaceStart && item.ReplaceEnd <= len(value) && (item.ReplaceStart != 0 || item.ReplaceEnd != 0) {
		m.input.SetValue(value[:item.ReplaceStart] + text + value[item.ReplaceEnd:])
		m.input.SetCursor(item.ReplaceStart + len(text))
		return
	}
	m.input.SetValue(text)
	m.input.CursorEnd()
}

func (m tuiModel) prevHistory() (tea.Model, tea.Cmd) {
	if len(m.history) == 0 {
		return m, nil
	}
	if m.histPos <= 0 {
		m.histPos = 0
	} else {
		m.histPos--
	}
	m.input.SetValue(m.history[m.histPos])
	m.input.CursorEnd()
	return m, nil
}

func (m tuiModel) nextHistory() (tea.Model, tea.Cmd) {
	if len(m.history) == 0 {
		return m, nil
	}
	if m.histPos >= len(m.history)-1 {
		m.histPos = len(m.history)
		m.input.SetValue("")
	} else {
		m.histPos++
		m.input.SetValue(m.history[m.histPos])
	}
	m.input.CursorEnd()
	return m, nil
}

func (m *tuiModel) appendUserContent(text string) {
	if strings.TrimSpace(m.content) != "" {
		m.content += "\n" + m.separatorLine() + "\n"
	}
	m.content += m.userName + ": " + text + "\n"
	m.assistantOpen = false
	m.reasoningOpen = false
	m.refreshContent()
}

func (m tuiModel) separatorLine() string {
	width := m.width
	if width <= 0 {
		width = 32
	}
	if width > 80 {
		width = 80
	}
	return strings.Repeat("─", max(8, width))
}

func (m *tuiModel) appendReasoningContent(text string) {
	text = strings.ReplaceAll(text, "[thinking] ", "")
	text = strings.ReplaceAll(text, "[thinking]", "")
	text = strings.ReplaceAll(text, "[/thinking]\n\n", "")
	text = strings.ReplaceAll(text, "[/thinking]", "")
	if text == "" {
		return
	}
	if !m.reasoningOpen {
		if strings.TrimSpace(m.content) != "" {
			m.content += "\n"
		}
		m.content += "thinking: "
		m.reasoningOpen = true
	}
	m.content += text
	m.refreshContent()
}

func (m *tuiModel) appendAssistantContent(text string) {
	if text == "" {
		return
	}
	if !m.assistantOpen {
		if m.reasoningOpen {
			m.content += "\n\n"
			m.reasoningOpen = false
		} else if strings.TrimSpace(m.content) != "" {
			m.content += "\n"
		}
		m.assistantStart = len(m.content)
		m.content += m.assistantName + ": "
		m.assistantOpen = true
	}
	m.content += text
	m.refreshContent()
}

func (m *tuiModel) replaceAssistantContent(text string) {
	if !m.assistantOpen {
		m.appendAssistantContent(text)
		return
	}
	if m.assistantStart < 0 || m.assistantStart > len(m.content) {
		m.assistantStart = len(m.content)
	}
	m.content = m.content[:m.assistantStart] + m.assistantName + ": " + text
	m.refreshContent()
}

func (m *tuiModel) finishAssistantContent() {
	m.assistantOpen = false
	m.assistantStart = 0
	m.reasoningOpen = false
	m.refreshContent()
}

func (m *tuiModel) appendContent(text string) {
	if text == "" {
		return
	}
	if m.reasoningOpen {
		if !strings.HasSuffix(m.content, "\n") {
			m.content += "\n"
		}
		m.reasoningOpen = false
	}
	m.content += text
	m.refreshContent()
}

func (m *tuiModel) appendNotice(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if m.layoutNoticeVisible() {
		m.notices = append(m.notices, text)
		if len(m.notices) > 50 {
			m.notices = m.notices[len(m.notices)-50:]
		}
		m.refreshNotices()
		return
	}
	m.appendContent("[notice] " + text + "\n")
}

func (m tuiModel) layoutNoticeVisible() bool {
	_, noticeWidth := m.layoutWidths()
	return noticeWidth > 0
}

func (m *tuiModel) refreshNotices() {
	if !m.layoutNoticeVisible() {
		return
	}
	if m.copyState.active() && m.copyState.Region == regionNotice {
		m.noticeViewport.SetContent(m.renderCopyLines(regionNotice, m.noticeCopyLines()))
		return
	}
	contentWidth := max(1, m.noticeViewport.Width-4)
	var sb strings.Builder
	sb.WriteString(tuiNoticeStyle.Bold(true).Render("通知"))
	if len(m.notices) == 0 {
		sb.WriteString("\n")
		sb.WriteString(tuiTitleMutedStyle.Render("暂无通知"))
		m.noticeViewport.SetContent(sb.String())
		return
	}
	for _, notice := range m.notices {
		sb.WriteString("\n\n")
		sb.WriteString(tuiNoticeStyle.Render("• "))
		sb.WriteString(wrapDisplayWidth(notice, contentWidth))
	}
	m.noticeViewport.SetContent(sb.String())
	m.noticeViewport.GotoBottom()
}

func (m *tuiModel) refreshContent() {
	content := m.wrappedContent()
	if m.copyState.active() && m.copyState.Region == regionChat {
		m.viewport.SetContent(m.renderCopyLines(regionChat, splitLines(content)))
		return
	}
	m.viewport.SetContent(m.renderContent(content))
	m.viewport.GotoBottom()
}

func (m *tuiModel) refreshCopyRegion() {
	switch m.copyState.Region {
	case regionChat:
		m.refreshContent()
	case regionNotice:
		m.refreshNotices()
	}
}

func (m tuiModel) renderContent(content string) string {
	if content == "" {
		return tuiTitleMutedStyle.Render("还没有消息。输入内容后按 Enter 发送。")
	}
	lines := strings.Split(content, "\n")
	inReasoning := false
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, m.userName+": "):
			inReasoning = false
			lines[i] = renderSpeakerLine(line, m.userName, tuiUserStyle)
		case strings.HasPrefix(line, m.assistantName+": "):
			inReasoning = false
			lines[i] = renderSpeakerLine(line, m.assistantName, tuiAssistantStyle)
		case strings.HasPrefix(line, "thinking: "):
			inReasoning = true
			lines[i] = tuiReasoningStyle.Render(line)
		case isSeparatorLine(line):
			inReasoning = false
			lines[i] = tuiSeparatorStyle.Render(line)
		case strings.HasPrefix(line, "[notice] "):
			inReasoning = false
		case inReasoning:
			lines[i] = tuiReasoningStyle.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) wrappedContent() string {
	if m.viewport.Width <= 1 {
		return m.content
	}
	return wrapDisplayWidth(m.content, m.viewport.Width)
}

func renderSpeakerLine(line, name string, style lipgloss.Style) string {
	prefix := name + ":"
	return style.Render(prefix) + strings.TrimPrefix(line, prefix)
}

func isSeparatorLine(line string) bool {
	line = strings.TrimSpace(line)
	return line != "" && strings.Trim(line, "─") == ""
}

func wrapDisplayWidth(text string, width int) string {
	var sb strings.Builder
	lineWidth := 0
	for _, r := range text {
		if r == '\n' {
			sb.WriteRune(r)
			lineWidth = 0
			continue
		}
		rw := runewidth.RuneWidth(r)
		if lineWidth > 0 && lineWidth+rw > width {
			sb.WriteRune('\n')
			lineWidth = 0
		}
		sb.WriteRune(r)
		lineWidth += rw
	}
	return sb.String()
}

func (m tuiModel) runtimeStatusText() string {
	if m.runtimeStatus.SessionID == "" && m.runtimeStatus.Provider == "" && m.runtimeStatus.Model == "" && m.runtimeStatus.Phase == "" {
		return ""
	}
	now := m.statusNow
	if now.IsZero() {
		now = time.Now()
	}
	width := m.width
	if width > 24 {
		width -= 24
	}
	return runtimestatus.FormatCompact(m.runtimeStatus, now, width)
}

func waitTUIOutput(output <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-output
		if !ok {
			return tea.Quit()
		}
		return msg
	}
}

func tickTUIStatus() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tuiStatusTickMsg(t)
	})
}
