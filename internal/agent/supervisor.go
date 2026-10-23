package agent

import (
	"context"
	"sync"

	agentevents "elbot/internal/agent/events"
	agentexecution "elbot/internal/agent/execution"
	agenthooks "elbot/internal/agent/hooks"
	agentinput "elbot/internal/agent/input"
	"elbot/internal/agent/routes"
	agentshare "elbot/internal/agent/share"
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
	identity        *agentshare.IdentityResolver
	bridge          *agenthooks.HookBridge
	bindings        *signalBindings
	mu              sync.Mutex
	started, closed bool
	done            chan struct{}
}

func (m *Supervisor) Handler() *agentinput.MessageHandler { return m.handler }

func (m *Supervisor) StatusChanged() *signal.Signal[agentevents.StatusChangedEvent, signal.Empty] {
	return m.statusChanged
}

func (m *Supervisor) Completion() *completion.Service { return m.completion }

func (m *Supervisor) Commands() *command.Router { return m.commands }

func (m *Supervisor) Sessions() *session.Service { return m.sessions }

func (m *Supervisor) Execution() *agentexecution.ExecutionCoordinator { return m.execution }

func (m *Supervisor) Identity() *agentshare.IdentityResolver { return m.identity }

func (m *Supervisor) ObserveHookRun() hook.Observer { return m.bridge.ObserveRun }
