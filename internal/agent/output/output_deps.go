package output

import (
	"context"

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
	Hooks interface {
		AgentOutputPrepared(context.Context, string) (string, globalevents.LogResult, error)
		AgentTurnOutputPrepared(context.Context, string) (string, globalevents.LogResult, error)
		PlatformMessageSent(context.Context, string)
	}
	Reasoning func(context.Context) delivery.ReasoningStream // Optional per-call presentation capability.
}
