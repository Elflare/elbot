package agent

import (
	agentevents "elbot/internal/agent/events"
	"elbot/internal/completion"
	"elbot/internal/signal"
)

// Agent exposes the application's message, execution and observation capabilities.
// Business state and dependencies belong to the components behind these entries.
type Agent struct {
	message        *messageHandler
	background     *backgroundRunner
	execution      *executionCoordinator
	fileCommands   *fileCommandPreparer
	identity       *identityResolver
	hooks          *hookBridge
	output         *outputSender
	status         *statusRecorder
	completion     *completion.Service
	signals        agentevents.Signals
	logConnections []*signal.Connection
}

func (a *Agent) Signals() agentevents.Signals { return a.signals }
