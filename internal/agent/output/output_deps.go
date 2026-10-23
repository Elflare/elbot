package output

import (
	"context"

	"elbot/internal/agent/dialogue"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
)

// OutputSenderDeps declares only the capabilities consumed by OutputSender.
type OutputSenderDeps struct {
	Dispatcher interface {
		SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error)
		SendChat(context.Context, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error)
		StartStream(context.Context) (delivery.MessageStream, error)
	}
	Hooks     dialogue.Hooks
	Reasoning func(context.Context) dialogue.ReasoningStream // Optional per-call presentation capability.
}
