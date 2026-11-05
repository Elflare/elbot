package output

import (
	"context"

	"elbot/internal/delivery"
)

// OutputSenderDeps declares only the capabilities consumed by OutputSender.
type OutputSenderDeps struct {
	Dispatcher interface {
		SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)
		StartStream(context.Context) (delivery.MessageStream, error)
	}
	Hooks interface {
		AgentOutputPrepared(context.Context, string) (string, error)
		AgentTurnOutputPrepared(context.Context, string) (string, error)
		PlatformMessageSent(context.Context, string)
	}
}
