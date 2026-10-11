package agent

import (
	"elbot/internal/agent/dialogue"
	"elbot/internal/delivery/dispatch"
)

// OutputSenderDeps declares only the capabilities consumed by OutputSender.
type OutputSenderDeps struct {
	Dispatcher *dispatch.Router
	Hooks      dialogue.Hooks
	Identity   dialogue.Identity
}
