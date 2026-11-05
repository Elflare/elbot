package execution

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"log/slog"

	"elbot/internal/delivery"

	"elbot/internal/turn"
)

// The sink changes when a running background execution is adopted. Already
// completed output is never replayed, and a request's cancellation is retained.
type executionTurnOutput struct {
	view interface {
		Context(context.Context) context.Context
	}
	foreground agentshare.Output
	execution  *turn.Execution
	fallback   agentshare.Output
}

func (o executionTurnOutput) target(ctx context.Context) (context.Context, agentshare.Output) {
	if o.execution.Foreground() != nil {
		return o.view.Context(ctx), o.foreground
	}
	return ctx, o.fallback
}

func (o executionTurnOutput) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	var foreground delivery.MessageStream
	return delivery.StreamFunc(func(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
		ctx, out := o.target(ctx)
		if o.execution.Foreground() == nil {
			return out.Send(ctx, event)
		}
		if foreground == nil {
			var err error
			foreground, err = out.StartStream(ctx)
			if err != nil {
				return delivery.Receipt{}, err
			}
		}
		return foreground.Send(ctx, event)
	}), nil
}

func (o executionTurnOutput) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	ctx, out := o.target(ctx)
	return out.Send(ctx, event)
}

func (o executionTurnOutput) Notify(ctx context.Context, l slog.Level, text string) {
	ctx, out := o.target(ctx)
	out.Notify(ctx, l, text)
}

func (o executionTurnOutput) SendPreview(ctx context.Context, text ...string) {
	ctx, out := o.target(ctx)
	out.SendPreview(ctx, text...)
}
