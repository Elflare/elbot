package execution

import (
	"context"
	"elbot/internal/operationresult"
	"log/slog"

	"elbot/internal/delivery"
	"elbot/internal/session"
)

type foregroundTurnOutput struct {
	ExecutionSender
}

type backgroundTurnOutput struct{}

func (o foregroundTurnOutput) Notify(ctx context.Context, level slog.Level, text string) {
	// A business result belongs to its captured conversation even after the
	// session changes. Live tool previews retain binding validation separately.
	o.ExecutionSender.Notify(session.WithBinding(ctx, nil), level, text)
}

func (o foregroundTurnOutput) SendPreview(ctx context.Context, text ...string) {
	o.ExecutionSender.Preview(ctx, text...)
}

func (o backgroundTurnOutput) StartStream(ctx context.Context) delivery.MessageStream {
	return delivery.StreamFunc(o.Send)
}

func (o backgroundTurnOutput) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func (o backgroundTurnOutput) Notify(ctx context.Context, level slog.Level, text string) {}

func (o backgroundTurnOutput) SendPreview(ctx context.Context, text ...string) {}

func (o backgroundTurnOutput) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (operationresult.Result[delivery.Receipt], error) {
	return operationresult.Result[delivery.Receipt]{Status: operationresult.Success}, nil
}
