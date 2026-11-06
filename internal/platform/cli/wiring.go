package cli

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
)

// wiring.go 负责构造 CLI 根适配器并连接终端默认能力。

// New creates a new CLI adapter.
func New(platformName string) *Adapter {
	return &Adapter{
		name: platformName, output: make(chan tea.Msg, 256),
		userName: "user", assistantName: "assistant", input: os.Stdin,
		interactive: isatty.IsTerminal(os.Stdin.Fd()),
		startDone:   make(chan struct{}), inputDone: make(chan struct{}),
		renderDone: make(chan struct{}), done: make(chan struct{}),
	}
}
