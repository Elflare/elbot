package cli

import (
	"context"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"elbot/internal/platform"
)

type tuiOutputMsg string
type tuiReplaceAssistantMsg string
type tuiFinishAssistantMsg struct{}
type tuiNoticeMsg string

type tuiProgramSetter func(*tea.Program)

const noticePanelWidth = 40

var (
	tuiTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)
	tuiTitleMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiStatusStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiUserStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	tuiAssistantStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("217"))
	tuiNoticeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	tuiSeparatorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiPanelStyle      = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(lipgloss.Color("8")).PaddingLeft(1)
)

type tuiModel struct {
	ctx     context.Context
	handler platform.PlatformHandler
	output  chan tea.Msg

	content              string
	notices              []string
	history              []string
	histPos              int
	userName             string
	assistantName        string
	assistantOpen        bool
	assistantStart       int
	viewport             viewport.Model
	noticeViewport       viewport.Model
	input                textinput.Model
	width                int
	height               int
	completionBase       string
	completionCandidates []string
	completionIndex      int
}

func runTUI(ctx context.Context, handler platform.PlatformHandler, output chan tea.Msg, setProgram tuiProgramSetter, userName, assistantName string) error {
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
		input:         input,
		userName:      userName,
		assistantName: assistantName,
	}
	program := tea.NewProgram(m)
	if setProgram != nil {
		setProgram(program)
		defer setProgram(nil)
	}
	// 默认不启用鼠标捕获，保留终端原生选择/复制能力。
	_, err := program.Run()
	return err
}

func (m tuiModel) Init() tea.Cmd {
	return waitTUIOutput(m.output)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		chatWidth, noticeWidth := m.layoutWidths()
		bodyHeight := max(1, msg.Height-4)
		m.viewport.Width = chatWidth
		m.viewport.Height = bodyHeight
		m.noticeViewport.Width = noticeWidth
		m.noticeViewport.Height = bodyHeight
		m.input.Width = msg.Width
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
	case tuiNoticeMsg:
		m.appendNotice(string(msg))
		return m, waitTUIOutput(m.output)
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
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
			return m.completeInput()
		case "up":
			m.clearCompletion()
			return m.prevHistory()
		case "down":
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

	oldInput := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != oldInput {
		m.clearCompletion()
	}
	return m, cmd
}

func (m tuiModel) View() string {
	return m.headerView() + "\n" + m.bodyView() + "\n" + m.statusView() + "\n" + m.input.View()
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
	status := "Ctrl+C/Esc exit · C-k/C-j chat · C-u/C-d notices · Up/Down history"
	if len(m.completionCandidates) > 1 {
		status += " · Tab " + strconv.Itoa(m.completionIndex+1) + "/" + strconv.Itoa(len(m.completionCandidates))
	}
	return tuiStatusStyle.Render(status)
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

func (m tuiModel) completeInput() (tea.Model, tea.Cmd) {
	c, ok := m.handler.(completer)
	if !ok {
		return m, nil
	}
	value := m.input.Value()
	if len(m.completionCandidates) > 0 && isCompletionCandidate(value, m.completionCandidates) {
		m.completionIndex = (m.completionIndex + 1) % len(m.completionCandidates)
		m.input.SetValue(m.completionCandidates[m.completionIndex])
		m.input.CursorEnd()
		return m, nil
	}
	candidates := c.Complete(value)
	switch len(candidates) {
	case 0:
		m.clearCompletion()
		return m, nil
	case 1:
		m.clearCompletion()
		m.input.SetValue(candidates[0])
		m.input.CursorEnd()
	default:
		m.completionBase = value
		m.completionCandidates = append([]string(nil), candidates...)
		m.completionIndex = 0
		m.input.SetValue(candidates[0])
		m.input.CursorEnd()
	}
	return m, nil
}

func isCompletionCandidate(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

func (m *tuiModel) clearCompletion() {
	m.completionBase = ""
	m.completionCandidates = nil
	m.completionIndex = 0
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

func (m *tuiModel) appendAssistantContent(text string) {
	if text == "" {
		return
	}
	if !m.assistantOpen {
		if strings.TrimSpace(m.content) != "" {
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
	m.refreshContent()
}

func (m *tuiModel) appendContent(text string) {
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
	m.viewport.SetContent(m.renderContent(m.wrappedContent()))
	m.viewport.GotoBottom()
}

func (m tuiModel) renderContent(content string) string {
	if content == "" {
		return tuiTitleMutedStyle.Render("还没有消息。输入内容后按 Enter 发送。")
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, m.userName+": "):
			lines[i] = renderSpeakerLine(line, m.userName, tuiUserStyle)
		case strings.HasPrefix(line, m.assistantName+": "):
			lines[i] = renderSpeakerLine(line, m.assistantName, tuiAssistantStyle)
		case isSeparatorLine(line):
			lines[i] = tuiSeparatorStyle.Render(line)
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

func waitTUIOutput(output <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-output
		if !ok {
			return tea.Quit()
		}
		return msg
	}
}
