package execution

import (
	"context"
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

func (o backgroundTurnOutput) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	return delivery.StreamFunc(o.Send), nil
}

func (o backgroundTurnOutput) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func (o backgroundTurnOutput) Notify(ctx context.Context, level slog.Level, text string) {}

func (o backgroundTurnOutput) SendPreview(ctx context.Context, text ...string) {}
