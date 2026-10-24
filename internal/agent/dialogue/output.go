package dialogue

import (
	"context"
	"log/slog"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/platform"
)

type Output interface {
	StartReasoning(context.Context) delivery.ReasoningStream
	StartStream(ctx context.Context) (delivery.MessageStream, error)
	FinishIntermediate(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error
	ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (delivery.Receipt, events.LogResult, error)
	SendAssistant(ctx context.Context, text string) (delivery.Receipt, events.LogResult, error)
	// SendOutputs returns the source log result and original business error;
	// partial delivery facts belong to the source log body, not a returned receipt.
	SendOutputs(ctx context.Context, outputs []delivery.Output) (events.LogResult, error)
	// Notify publishes a business fact to its captured conversation; callers
	// choose its lifetime. SendPreview remains tied to the active operation.
	Notify(ctx context.Context, level slog.Level, text string)
	// SendPreview takes one complete notice per tool call, including multiline
	// bodies; pass batch notices separately rather than joining them. General
	// workflow notices occupy one argument. Nonblank notices get one prefix each
	// and are joined before a single output Hook and notification.
	SendPreview(ctx context.Context, text ...string)
}

func BufferAssistantOutput(ctx context.Context) bool {
	msg, ok := platform.MessageContextFrom(ctx)
	return ok && msg.BufferAssistantOutput
}

type AssistantPreparer interface {
	PrepareAssistant(context.Context, hook.Point, string) (string, events.LogResult, error)
}
