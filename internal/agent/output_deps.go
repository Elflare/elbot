package agent

import (
	"elbot/internal/agent/dialogue"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/notification"
)

// OutputSenderDeps declares only the capabilities consumed by OutputSender.
type OutputSenderDeps struct {
	Dispatcher    *dispatch.Router
	Notifications *notification.Manager
	Hooks         dialogue.Hooks
	Identity      dialogue.Identity
}
