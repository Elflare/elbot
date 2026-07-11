package cli

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
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
	tuiInputCharLimit       = 4096
	tuiInputGutterWidth     = 2
	minTUIInputHeight       = 3
	maxTUIInputHeight       = 6
	pasteBurstCharInterval  = 12 * time.Millisecond
	pasteBurstIdleTimeout   = 80 * time.Millisecond
	pasteBurstMinRunes      = 3
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
	tuiInputPromptStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	tuiInputSeparatorStyle     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#989898", Dark: "#666666"})
)

type legacyCompleter interface {
	Complete(text string) []string
}

type completionState struct {
	base  string
	items []completion.Item
	index int
}

type tuiPasteBurst struct {
	lastInput time.Time
	runes     int
	active    bool
}

// Windows Console Input exposes pasted text as ordinary key events. Detect a
// short rune burst so Enter inside that stream becomes a textarea newline.
func (b *tuiPasteBurst) observeText(now time.Time, count int) {
	if count <= 0 {
		return
	}
	gap := now.Sub(b.lastInput)
	if b.lastInput.IsZero() || gap < 0 || gap > pasteBurstIdleTimeout {
		b.runes = count
		b.active = false
	} else if b.active || gap <= pasteBurstCharInterval {
		b.runes += count
	} else {
		b.runes = count
	}
	if b.runes >= pasteBurstMinRunes {
		b.active = true
	}
	b.lastInput = now
}

func (b *tuiPasteBurst) shouldInsertEnter(now time.Time) bool {
	if !b.active || b.lastInput.IsZero() || now.Sub(b.lastInput) > pasteBurstIdleTimeout {
		b.reset()
		return false
	}
	b.lastInput = now
	return true
}

func (b *tuiPasteBurst) reset() {
	b.lastInput = time.Time{}
	b.runes = 0
	b.active = false
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
	completion      completionProvider
	localFiles      *localFileResolver
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
	input           textarea.Model
	pasteBurst      tuiPasteBurst
	inputNow        func() time.Time
	width           int
	height          int
}

type completionProvider interface {
	Complete(context.Context, completion.Request) []completion.Item
}

func newTUIInput() textarea.Model {
	input := textarea.New()
	input.Focus()
	input.FocusedStyle.CursorLine = input.FocusedStyle.Text
	input.Prompt = ""
	input.ShowLineNumbers = false
	input.CharLimit = tuiInputCharLimit
	input.SetHeight(1)
	return input
}

func runTUI(ctx context.Context, handler platform.PlatformHandler, completion completionProvider, output chan tea.Msg, setProgram tuiProgramSetter, userName, assistantName string) error {
	input := newTUIInput()

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
		localFiles:    newLocalFileResolver(""),
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
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.Paste {
		keyMsg.Runes = []rune(normalizeTUIInputPaste(string(keyMsg.Runes)))
		msg = keyMsg
	}
	if keyMsg, ok := msg.(tea.KeyMsg); ok && runtime.GOOS == "windows" {
		now := m.now()
		if !keyMsg.Paste && !keyMsg.Alt && len(keyMsg.Runes) > 0 {
			m.pasteBurst.observeText(now, len(keyMsg.Runes))
		} else if keyMsg.Type != tea.KeyEnter {
			m.pasteBurst.reset()
		}
	}
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
	case tuiClipboardMsg:
		if msg.err != nil {
			m.appendNotice("paste: " + msg.err.Error())
			return m, nil
		}
		oldInputContentHeight := m.inputContentHeight()
		m.input.InsertString(normalizeTUIInputPaste(msg.text))
		m.clearCompletion()
		if m.inputContentHeight() != oldInputContentHeight {
			m.resizeViewports()
			m.refreshContent()
			m.refreshNotices()
		}
		return m, nil
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
				m.clearInput()
			}
			return m, nil
		case "ctrl+c":
			if m.completionState.visible() {
				m.clearCompletion()
				return m, nil
			}
			if m.input.Value() != "" {
				m.clearInput()
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+v":
			return m, readTUIClipboard()
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
		case "tab", "ctrl+i":
			return m.completeInput(1)
		case "shift+tab", "backtab":
			return m.completeInput(-1)
		case "up":
			if m.completionState.visible() {
				m.selectCompletion(-1)
				return m, nil
			}
			if m.input.Line() == 0 {
				m.clearCompletion()
				return m.prevHistory()
			}
		case "down":
			if m.completionState.visible() {
				m.selectCompletion(1)
				return m, nil
			}
			if m.input.Line() == m.input.LineCount()-1 {
				m.clearCompletion()
				return m.nextHistory()
			}
		case "enter":
			if runtime.GOOS == "windows" && m.pasteBurst.shouldInsertEnter(m.now()) {
				break
			}
			if m.input.Line() == m.input.LineCount()-1 {
				return m.submitInput()
			}
		}
	}

	if m.copyState.active() {
		return m, nil
	}
	oldInput := m.input.Value()
	oldInputContentHeight := m.inputContentHeight()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != oldInput {
		m.clearCompletion()
		if m.inputContentHeight() != oldInputContentHeight {
			m.resizeViewports()
			m.refreshContent()
			m.refreshNotices()
		}
	}
	return m, cmd
}

func (m tuiModel) View() string {
	return m.headerView() + "\n" + m.bodyView() + m.completionView() + "\n" + m.statusView() + "\n" + m.inputSeparatorView() + "\n" + m.inputView()
}

func normalizeTUIInputPaste(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func (m *tuiModel) resizeViewports() {
	chatWidth, noticeWidth := m.layoutWidths()
	m.input.SetWidth(max(1, m.width-tuiInputGutterWidth))
	inputHeight := m.inputViewHeight()
	bodyHeight := max(1, m.height-4-m.completionViewHeight()-inputHeight)
	m.viewport.Width = chatWidth
	m.viewport.Height = bodyHeight
	m.noticeViewport.Width = noticeWidth
	m.noticeViewport.Height = bodyHeight
	m.input.SetHeight(min(m.inputContentHeight(), maxTUIInputHeight))
}

func (m tuiModel) inputViewHeight() int {
	return min(max(minTUIInputHeight, m.inputContentHeight()), maxTUIInputHeight)
}

func (m tuiModel) inputContentHeight() int {
	width := max(1, m.input.Width())
	height := 0
	for _, line := range strings.Split(m.input.Value(), "\n") {
		height += max(1, strings.Count(wrapDisplayWidth(line, width), "\n")+1)
	}
	return max(1, height)
}

func (m tuiModel) inputView() string {
	height := m.inputViewHeight()
	prompt := lipgloss.PlaceVertical(height, lipgloss.Center, tuiInputPromptStyle.Render("❯ "))
	content := lipgloss.PlaceVertical(height, lipgloss.Center, m.input.View())
	return lipgloss.JoinHorizontal(lipgloss.Top, prompt, content)
}

func (m tuiModel) inputSeparatorView() string {
	if m.width <= 0 {
		return ""
	}
	padding := max(1, m.width/20)
	minSeparatorWidth := min(m.width, max(8, m.width/3))
	keys := m.inputShortcutText()
	maxKeysWidth := max(0, m.width-padding*2-minSeparatorWidth)
	keys = truncateLeftDisplayWidth(keys, maxKeysWidth)
	separatorWidth := max(0, m.width-padding*2-runewidth.StringWidth(keys))
	return strings.Repeat(" ", padding) +
		tuiInputSeparatorStyle.Render(strings.Repeat("─", separatorWidth)) +
		strings.Repeat(" ", padding) +
		tuiStatusStyle.Render(keys)
}

func (m tuiModel) inputShortcutText() string {
	if _, noticeWidth := m.layoutWidths(); noticeWidth > 0 {
		return "Alt+h/l copy · Esc clear · Ctrl+C exit"
	}
	return "Alt+h copy · Esc clear · Ctrl+C exit"
}

func truncateLeftDisplayWidth(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(text)
	used := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		runeWidth := runewidth.RuneWidth(runes[i])
		if used+runeWidth > width-1 {
			break
		}
		used += runeWidth
		start = i
	}
	return "…" + string(runes[start:])
}

func (m tuiModel) now() time.Time {
	if m.inputNow != nil {
		return m.inputNow()
	}
	return time.Now()
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
	return lipgloss.NewStyle().Width(chatWidth).Render(chatTitle)
}

func (m tuiModel) statusView() string {
	if m.copyState.active() {
		return tuiStatusStyle.Render(m.copyStatusText())
	}
	return tuiStatusStyle.Render(m.runtimeStatusText())
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
	cursor := byteOffsetForRunePosition(value, textareaCursorRunePosition(m.input))
	if resolver := m.localFileResolver(); resolver != nil {
		items := resolver.Complete(m.ctx, completion.Request{Text: value, Cursor: cursor})
		if len(items) > 0 {
			return items
		}
	}
	if m.completion != nil {
		return m.completion.Complete(m.ctx, completion.Request{Text: value, Cursor: cursor})
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

func (m tuiModel) localFileResolver() *localFileResolver {
	if m.localFiles != nil {
		return m.localFiles
	}
	return newLocalFileResolver("")
}

func (m tuiModel) expandLocalFileReferences(text string) (string, error) {
	resolver := m.localFileResolver()
	if resolver == nil {
		return text, nil
	}
	return resolver.expandReferences(text)
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
		updated := value[:item.ReplaceStart] + text + value[item.ReplaceEnd:]
		setTextareaValueAndCursor(&m.input, updated, runePositionForByteOffset(updated, item.ReplaceStart+len(text)))
		return
	}
	m.input.SetValue(text)
}

func textareaCursorRunePosition(input textarea.Model) int {
	lines := strings.Split(input.Value(), "\n")
	row := min(max(0, input.Line()), len(lines)-1)
	position := 0
	for i := 0; i < row; i++ {
		position += len([]rune(lines[i])) + 1
	}
	lineInfo := input.LineInfo()
	return position + lineInfo.StartColumn + lineInfo.ColumnOffset
}

func setTextareaValueAndCursor(input *textarea.Model, value string, runePosition int) {
	runes := []rune(value)
	runePosition = min(max(0, runePosition), len(runes))
	row := 0
	column := 0
	for _, r := range runes[:runePosition] {
		if r == '\n' {
			row++
			column = 0
		} else {
			column++
		}
	}
	input.SetValue(value)
	for input.Line() > row {
		input.CursorUp()
	}
	input.SetCursor(column)
}

func byteOffsetForRunePosition(value string, pos int) int {
	if pos <= 0 {
		return 0
	}
	count := 0
	for offset := range value {
		if count == pos {
			return offset
		}
		count++
	}
	return len(value)
}

func runePositionForByteOffset(value string, offset int) int {
	if offset <= 0 {
		return 0
	}
	count := 0
	for byteOffset := range value {
		if byteOffset >= offset {
			return count
		}
		count++
	}
	return count
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
	m.resizeViewports()
	m.refreshContent()
	m.refreshNotices()
	return m, nil
}

func (m tuiModel) nextHistory() (tea.Model, tea.Cmd) {
	if len(m.history) == 0 {
		return m, nil
	}
	if m.histPos >= len(m.history)-1 {
		m.histPos = len(m.history)
		m.clearInput()
	} else {
		m.histPos++
		m.input.SetValue(m.history[m.histPos])
		m.resizeViewports()
		m.refreshContent()
		m.refreshNotices()
	}
	return m, nil
}

func (m tuiModel) submitInput() (tea.Model, tea.Cmd) {
	m.clearCompletion()
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	if text == "/exit" {
		return m, tea.Quit
	}
	sendText, err := m.expandLocalFileReferences(text)
	if err != nil {
		m.input.SetValue(m.input.Value())
		m.appendNotice("local file reference: " + err.Error())
		return m, nil
	}
	m.history = append(m.history, text)
	m.histPos = len(m.history)
	m.clearInput()
	m.appendUserContent(text)
	go func() {
		if err := m.handler.HandleMessage(m.ctx, sendText); err != nil {
			select {
			case m.output <- tuiNoticeMsg("error: " + err.Error()):
			case <-m.ctx.Done():
			}
		}
	}()
	return m, nil
}

func (m *tuiModel) clearInput() {
	m.input.Reset()
	m.resizeViewports()
	m.refreshContent()
	m.refreshNotices()
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
	m.appendNoticeContent(text)
}

func (m *tuiModel) appendNoticeContent(text string) {
	if strings.TrimSpace(m.content) != "" {
		m.content += "\n" + m.separatorLine() + "\n"
	}
	m.content += "[notice] " + text
	m.assistantOpen = false
	m.reasoningOpen = false
	m.refreshContent()
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
	sb.WriteString(tuiNoticeStyle.Bold(true).Render("Notices"))
	if len(m.notices) == 0 {
		sb.WriteString("\n")
		sb.WriteString(tuiTitleMutedStyle.Render("暂无通知"))
		m.noticeViewport.SetContent(sb.String())
		return
	}
	separator := tuiSeparatorStyle.Render(strings.Repeat("─", max(8, contentWidth)))
	for _, notice := range m.notices {
		sb.WriteString("\n")
		sb.WriteString(separator)
		sb.WriteString("\n")
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
