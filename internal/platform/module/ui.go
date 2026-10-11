package module

import (
	"context"
	"errors"

	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	"elbot/internal/platform/cli"
	"elbot/internal/session"
	"elbot/internal/signal"
)

// ConnectUI assembles CLI presentation after the backend has been constructed.
func (m *Module) ConnectUI(status *signal.Signal[agentevents.StatusChangedEvent, signal.Empty], binding *signal.Signal[session.BindingChangedEvent, signal.Empty]) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return signal.ErrClosed
	}
	if m.started {
		return errors.New("CLI UI must connect before platforms start")
	}
	if m.ui != nil {
		return nil
	}
	for _, runtime := range m.runtimes {
		switch runtime.(type) {
		case *cli.Adapter, *cli.RemoteServer:
			var err error
			m.ui, err = cli.ConnectUI(status, binding)
			return err
		}
	}
	return nil
}

func (m *Module) StartReasoning(ctx context.Context) dialogue.ReasoningStream {
	m.mu.Lock()
	u := m.ui
	m.mu.Unlock()
	if u == nil {
		return nil
	}
	return u.StartReasoning(ctx)
}
