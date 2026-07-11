package cli

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
	if w.out == nil {
		return fmt.Errorf("osc52 output is nil")
	}
	maxBytes := w.maxBytes
	if maxBytes <= 0 {
		maxBytes = osc52ClipboardMaxBytes
	}
	if len([]byte(text)) > maxBytes {
		return fmt.Errorf("copy text too large for OSC52: %d bytes > %d bytes", len([]byte(text)), maxBytes)
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
	if w.remoteSession() {
		if err := w.writeOSC52(text); err == nil {
			return nil
		}
		return w.writeSystem(text)
	}
	if err := w.writeSystem(text); err == nil {
		return nil
	}
	return w.writeOSC52(text)
}

func (w autoClipboardWriter) writeSystem(text string) error {
	if w.system == nil {
		return fmt.Errorf("system clipboard is nil")
	}
	return w.system.WriteAll(text)
}

func (w autoClipboardWriter) writeOSC52(text string) error {
	if w.osc52 == nil {
		return fmt.Errorf("osc52 clipboard is nil")
	}
	return w.osc52.WriteAll(text)
}

func (w autoClipboardWriter) remoteSession() bool {
	env := w.env
	if env == nil {
		env = os.Getenv
	}
	return env("SSH_CONNECTION") != "" || env("SSH_TTY") != "" || env("TMUX") != "" || env("STY") != ""
}
