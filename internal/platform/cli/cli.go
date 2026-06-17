package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"

	"elbot/internal/completion"
	"elbot/internal/output"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
)

// Adapter is a CLI platform adapter that reads from stdin.
type Adapter struct {
	mu            sync.Mutex
	output        chan tea.Msg
	program       *tea.Program
	userName      string
	assistantName string
	connectNotify func(context.Context, string)
	completion    *completion.Service
}

type cliMessageStream struct {
	adapter *Adapter
}

// New creates a new CLI adapter.
func New() *Adapter {
	return &Adapter{output: make(chan tea.Msg, 256), userName: "user", assistantName: "assistant"}
}

// Name returns the platform name.
func (a *Adapter) Name() string {
	return "cli"
}

func (a *Adapter) SetCompleter(service *completion.Service) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.completion = service
}

func (a *Adapter) SetConnectNotifier(notify func(context.Context, string)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connectNotify = notify
}

// StopAppOnExit marks the interactive CLI as owning the current foreground process.
// Future service deployments can use adapters that return false so CLI exit won't stop services.
func (a *Adapter) StopAppOnExit() bool {
	return true
}

// Run starts the stdin read loop. It handles /exit locally and forwards
// all other input to the handler.
func (a *Adapter) Run(ctx context.Context, handler platform.PlatformHandler) error {
	a.notifyConnected(ctx)
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return a.runScanner(ctx, handler)
	}
	return a.runInteractive(ctx, handler)
}

func (a *Adapter) runScanner(ctx context.Context, handler platform.PlatformHandler) error {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("> ")
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		text := scanner.Text()
		if text == "/exit" {
			return nil
		}

		if err := handler.HandleMessage(ctx, text); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}

		fmt.Print("> ")
	}
	return scanner.Err()
}

func (a *Adapter) runInteractive(ctx context.Context, handler platform.PlatformHandler) error {
	a.mu.Lock()
	completion := a.completion
	a.mu.Unlock()
	return runTUI(ctx, handler, completion, a.output, a.setProgram, a.userName, a.assistantName)
}

func (a *Adapter) setProgram(program *tea.Program) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.program = program
}

func (a *Adapter) notifyConnected(ctx context.Context) {
	a.mu.Lock()
	notify := a.connectNotify
	a.mu.Unlock()
	if notify != nil {
		notify(ctx, a.Name())
	}
}

func (a *Adapter) StartStream(ctx context.Context) (platform.MessageStream, error) {
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return nil, fmt.Errorf("cli streaming output requires interactive TUI")
	}
	a.mu.Lock()
	program := a.program
	a.mu.Unlock()
	if program == nil {
		return nil, fmt.Errorf("cli TUI is not running")
	}
	return cliMessageStream{adapter: a}, nil
}

func (s cliMessageStream) Append(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	s.adapter.sendTUIMessage(tuiOutputMsg(text), text)
	return nil
}

func (s cliMessageStream) Replace(ctx context.Context, text string) (platform.Receipt, error) {
	s.adapter.sendTUIMessage(tuiReplaceAssistantMsg(text), text)
	return platform.Receipt{}, nil
}

func (s cliMessageStream) Finish(ctx context.Context) (platform.Receipt, error) {
	s.adapter.sendTUIMessage(tuiFinishAssistantMsg{}, "\n")
	return platform.Receipt{}, nil
}

func (a *Adapter) SetRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) error {
	a.sendTUIMessage(tuiStatusMsg(snapshot), runtimestatus.FormatCompact(snapshot, time.Now(), 0))
	return nil
}

func (a *Adapter) SendReasoning(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	a.sendTUIMessage(tuiReasoningMsg(text), text)
	return nil
}

func (a *Adapter) SendChat(ctx context.Context, out output.Output) (platform.Receipt, error) {
	text := chatText(out)
	if text != "" {
		a.sendTUIMessage(tuiOutputMsg(text), text)
	}
	return platform.Receipt{}, nil
}

func chatText(out output.Output) string {
	if out.Kind == output.KindText {
		return out.Text
	}
	return output.FallbackText(out)
}

func (a *Adapter) SendNotice(ctx context.Context, target output.Target, out output.Output) (platform.Receipt, error) {
	if platformName := strings.TrimSpace(target.Platform); platformName != "" && platformName != a.Name() {
		return platform.Receipt{}, fmt.Errorf("cli cannot send to platform %q", platformName)
	}
	text := output.FallbackText(out)
	if text != "" {
		a.sendTUIMessage(tuiNoticeMsg(text), "[notice] "+text)
	}
	return platform.Receipt{}, nil
}

func (a *Adapter) SendToolNotice(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	_, _ = a.SendNotice(context.Background(), output.Target{}, output.Text("[tool] "+text))
}

func (a *Adapter) sendTUIMessage(msg tea.Msg, fallback string) {
	a.mu.Lock()
	output := a.output
	program := a.program
	a.mu.Unlock()
	if output == nil || !isatty.IsTerminal(os.Stdin.Fd()) {
		printLine(fallback)
		return
	}
	if program != nil {
		program.Send(msg)
		return
	}
	select {
	case output <- msg:
	default:
		printLine(fallback)
	}
}

func printLine(text string) {
	if text == "" {
		return
	}
	fmt.Print(text)
	if !strings.HasSuffix(text, "\n") {
		fmt.Println()
	}
}
