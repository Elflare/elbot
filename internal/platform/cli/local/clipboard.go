// clipboard.go 连接系统剪贴板，支持终端复制和粘贴。

package local

import (
	"fmt"
	"io"
	"os"

	"github.com/atotto/clipboard"
	osc52 "github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"
)

const osc52ClipboardMaxBytes = 100 * 1024

type clipboardWriter interface {
	WriteAll(string) error
}

type tuiClipboardMsg struct {
	text string
	err  error
}

func readTUIClipboard() tea.Cmd {
	return func() tea.Msg {
		text, err := clipboard.ReadAll()
		return tuiClipboardMsg{text: text, err: err}
	}
}

type systemClipboardWriter struct{}

func (systemClipboardWriter) WriteAll(text string) error {
	return clipboard.WriteAll(text)
}

type osc52ClipboardWriter struct {
	out      io.Writer
	maxBytes int
}

func (w osc52ClipboardWriter) WriteAll(text string) error {
	maxBytes := w.maxBytes
	if maxBytes <= 0 {
		maxBytes = osc52ClipboardMaxBytes
	}
	if len(text) > maxBytes {
		return fmt.Errorf("copy text too large for OSC52: %d bytes > %d bytes", len(text), maxBytes)
	}
	_, err := io.WriteString(w.out, osc52.New(text).String())
	return err
}

type autoClipboardWriter struct {
	system clipboardWriter
	osc52  clipboardWriter
	env    func(string) string
}

func newClipboardWriter() clipboardWriter {
	return autoClipboardWriter{
		system: systemClipboardWriter{},
		osc52:  osc52ClipboardWriter{out: os.Stdout},
		env:    os.Getenv,
	}
}

func (w autoClipboardWriter) WriteAll(text string) error {
	first, second := w.system, w.osc52
	if w.remoteSession() {
		first, second = second, first
	}
	if err := first.WriteAll(text); err != nil {
		return second.WriteAll(text)
	}
	return nil
}

func (w autoClipboardWriter) remoteSession() bool {
	env := w.env
	return env("SSH_CONNECTION") != "" || env("SSH_TTY") != "" || env("TMUX") != "" || env("STY") != ""
}
