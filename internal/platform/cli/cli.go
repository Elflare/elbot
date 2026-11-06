// Package cli 是 CLI 平台入口，负责对外能力与本地、远程实现的装配。
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"elbot/internal/completion"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
)

// Name returns the platform name.
func (a *Adapter) Name() string {
	return a.name
}

func (a *Adapter) SetCompleter(service func(context.Context, completion.Request) ([]completion.Item, error)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.completion = service
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
	a.mu.Lock()
	program := a.program
	a.mu.Unlock()
	if a.interactive && program == nil {
		return nil, fmt.Errorf("cli TUI is not running")
	}
	return cliMessageStream{adapter: a, id: nextStreamID.Add(1)}, nil
}

func (s cliMessageStream) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	if !s.adapter.interactive && event.Type != delivery.StreamSend {
		return delivery.Receipt{}, nil
	}
	return delivery.Receipt{}, s.adapter.sendTUIMessage(tuiStreamMsg{ID: s.id, Event: event}, event.Text)
}

func outputsText(outputs []delivery.Output) string {
	return delivery.FallbackOutput(outputs).Text
}

func (a *Adapter) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	if platformName := strings.TrimSpace(target.Platform); platformName != "" && platformName != a.Name() {
		return delivery.Receipt{}, fmt.Errorf("cli cannot send to platform %q", platformName)
	}
	text := outputsText(outputs)
	if text != "" {
		if err := a.sendTUIMessage(tuiOutputMsg(text), text); err != nil {
			return delivery.Receipt{}, err
		}
	}
	return delivery.Receipt{}, nil
}

func (a *Adapter) sendTUIMessage(msg tea.Msg, fallback string) error {
	a.mu.Lock()
	output := a.output
	program := a.program
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return io.ErrClosedPipe
	}
	if !a.interactive {
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
