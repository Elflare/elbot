package agent

import (
	"context"
	"sync"

	agentevents "elbot/internal/agent/events"
	agentexecution "elbot/internal/agent/execution"
	agenthooks "elbot/internal/agent/hooks"
	agentinput "elbot/internal/agent/input"
	"elbot/internal/agent/routes"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/contextmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/turn"
)

// Supervisor owns the foreground business line and its independently managed Session, Request and Turn state.
type Supervisor struct {
	signals         agentevents.Signals // Owned event sources; closed after internal producers finish.
	ctx             context.Context
	cancel          context.CancelFunc
	sessions        *session.Service
	requests        *request.Manager
	turns           *turn.Manager
	routes          *routes.Registry
	contexts        *contextmgr.Service
	commands        *command.Router
	handler         *agentinput.MessageHandler
	completion      *completion.Service
	execution       *agentexecution.ExecutionCoordinator
	bridge          *agenthooks.HookBridge
	bindings        *signalBindings
	mu              sync.Mutex
	started, closed bool
	done            chan struct{}
}

func (m *Supervisor) HandleMessage(ctx context.Context, text string) error {
	return m.handler.HandleMessage(ctx, text)
}

func (m *Supervisor) Complete(ctx context.Context, req completion.Request) ([]completion.Item, error) {
	return m.completion.Complete(ctx, req)
}

// Commands returns detached metadata, never the mutable command router.
func (m *Supervisor) Commands() []command.Info {
	commands := m.commands.Commands()
	for i := range commands {
		commands[i].Aliases = append([]string(nil), commands[i].Aliases...)
	}
	return commands
}

// ObserveStatusChanged gives each subscriber detached facts without the internal context.
// Handlers update memory or enqueue work; the caller owns its subscription and work lifecycle.
func (m *Supervisor) ObserveStatusChanged(handler func(agentevents.StatusChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, signal.ErrClosed
	}
	return m.signals.StatusChanged.Observe(func(_ context.Context, event agentevents.StatusChangedEvent) error {
		event.Snapshot.Usage = agentevents.CloneUsage(event.Snapshot.Usage)
		if event.Conversation != nil {
			conversation := *event.Conversation
			event.Conversation = &conversation
		}
		if event.Binding != nil {
			binding := *event.Binding
			event.Binding = &binding
		}
		return handler(event)
	}, options)
}

// ObserveBindingChanged projects internal activation handles into detached facts.
// Handlers update memory or enqueue work; the caller owns its subscription and work lifecycle.
func (m *Supervisor) ObserveBindingChanged(handler func(agentevents.BindingChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, signal.ErrClosed
	}
	return m.sessions.BindingChanged().Observe(func(_ context.Context, event session.BindingChangedEvent) error {
		snapshot := agentevents.BindingChangedEvent{Version: event.Version}
		if event.Old != nil {
			snapshot.Old = &agentevents.BindingSnapshot{Scope: event.Old.Scope(), SessionID: event.Old.SessionID(), Version: event.Old.Version()}
		}
		if event.New != nil {
			snapshot.New = &agentevents.BindingSnapshot{Scope: event.New.Scope(), SessionID: event.New.SessionID(), Version: event.New.Version()}
		}
		return handler(snapshot)
	}, options)
}
