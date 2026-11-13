package output

import (
	"context"
	"elbot/internal/operationresult"

	"elbot/internal/delivery"
)

// OutputSenderDeps declares only the capabilities consumed by OutputSender.
type OutputSenderDeps struct {
	Dispatcher interface {
		SendTo(context.Context, delivery.Target, []delivery.Output) (operationresult.Result[delivery.Receipt], error)
		StartStream(context.Context) (operationresult.Result[delivery.MessageStream], error)
	}
	Hooks interface {
		AgentOutputPrepared(context.Context, string) (string, error)
		AgentTurnOutputPrepared(context.Context, string) (string, error)
		PlatformMessageSent(context.Context, string)
	}
}
