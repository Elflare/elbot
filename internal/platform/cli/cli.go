// cli.go 实现 CLI 适配器运行、输入接入与消息发送。

package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"

	"elbot/internal/completion"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/platform"
)

// Adapter is a CLI platform adapter that reads from stdin.
type Adapter struct {
	mu            sync.Mutex
	output        chan tea.Msg
	program       *tea.Program
	userName      string
	assistantName string
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

// StopAppOnExit marks the interactive CLI as owning the current foreground process.
// Future service deployments can use adapters that return false so CLI exit won't stop services.
func (a *Adapter) StopAppOnExit() bool {
	return true
}

// Run starts the stdin read loop. It handles /exit locally and forwards
// all other input to the handler.
func (a *Adapter) Run(ctx context.Context, handler platform.PlatformHandler) error {
	handler = localMessageHandler{next: handler, adapter: a}
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

		_ = handler.HandleMessage(ctx, text)

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
	// Subscribers hand background work to their own business queues.
	_, _ = globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: a.Name()})
}

func (a *Adapter) StartStream(ctx context.Context) (delivery.MessageStream, error) {
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

func (s cliMessageStream) Replace(ctx context.Context, text string) (delivery.Receipt, globalevents.LogResult, error) {
	s.adapter.sendTUIMessage(tuiReplaceAssistantMsg(text), text)
	return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (s cliMessageStream) Finish(ctx context.Context) (delivery.Receipt, globalevents.LogResult, error) {
	s.adapter.sendTUIMessage(tuiFinishAssistantMsg{}, "\n")
	return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	text := outputsText(outputs)
	if text != "" {
		a.sendTUIMessage(tuiOutputMsg(text), text)
	}
	return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func outputsText(outputs []delivery.Output) string {
	return delivery.FallbackOutput(outputs).Text
}

func (a *Adapter) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	if platformName := strings.TrimSpace(target.Platform); platformName != "" && platformName != a.Name() {
		{
			err := fmt.Errorf("cli cannot send to platform %q", platformName)
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			return delivery.Receipt{}, logResult, err
		}
	}
	text := outputsText(outputs)
	if text != "" {
		a.sendTUIMessage(tuiOutputMsg(text), text)
	}
	return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Adapter) sendTUIMessage(msg tea.Msg, fallback string) error {
	a.mu.Lock()
	output := a.output
	program := a.program
	a.mu.Unlock()
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		_, err := fmt.Fprintln(os.Stdout, strings.TrimSuffix(fallback, "\n"))
		return err
	}
	if program != nil {
		program.Send(msg)
		return nil
	}
	select {
	case output <- msg:
	default:
		_, err := fmt.Fprintln(os.Stdout, strings.TrimSuffix(fallback, "\n"))
		return err
	}
	return nil
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
