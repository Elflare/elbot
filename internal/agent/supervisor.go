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
	"elbot/internal/hook"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/turn"
)

// Supervisor owns the foreground business line and its independently managed Session, Request and Turn state.
type Supervisor struct {
	statusChanged   *signal.Signal[agentevents.StatusChangedEvent, signal.Empty]
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

func (m *Supervisor) HookWakeup(ctx context.Context, event hook.Event) bool {
	return m.handler.HookWakeup(ctx, event)
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

func (m *Supervisor) ObserveHookRun(ctx context.Context, event hook.Event, info hook.ObserverInfo) (context.Context, func()) {
	return m.bridge.ObserveRun(ctx, event, info)
}

// ObserveStatusChanged gives the caller ownership of its subscription only.
func (m *Supervisor) ObserveStatusChanged(handler func(context.Context, agentevents.StatusChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	return m.statusChanged.Observe(func(ctx context.Context, event agentevents.StatusChangedEvent) error {
		event.Snapshot.Usage = agentevents.CloneUsage(event.Snapshot.Usage)
		return handler(ctx, event)
	}, options)
}

// ObserveBindingChanged projects internal activation handles into detached facts.
func (m *Supervisor) ObserveBindingChanged(handler func(context.Context, agentevents.BindingChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	return m.sessions.BindingChanged().Observe(func(ctx context.Context, event session.BindingChangedEvent) error {
		snapshot := agentevents.BindingChangedEvent{Reason: event.Reason}
		if event.Old != nil {
			snapshot.Old = &agentevents.BindingSnapshot{Scope: event.Old.Scope(), SessionID: event.Old.SessionID()}
		}
		if event.New != nil {
			snapshot.New = &agentevents.BindingSnapshot{Scope: event.New.Scope(), SessionID: event.New.SessionID()}
		}
		return handler(ctx, snapshot)
	}, options)
}
