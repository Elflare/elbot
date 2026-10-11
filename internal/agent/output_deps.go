package agent

import (
	"context"
	"elbot/internal/agent/dialogue"
	"elbot/internal/delivery/dispatch"
)

// OutputSenderDeps declares only the capabilities consumed by OutputSender.
type OutputSenderDeps struct {
	Dispatcher *dispatch.Router
	Hooks      dialogue.Hooks
	Reasoning  func(context.Context) dialogue.ReasoningStream // Optional per-call presentation capability.
}
