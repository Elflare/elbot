package execution

import (
	agentshare "elbot/internal/agent/share"
	"elbot/internal/operationresult"

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

func (o executionTurnOutput) StartStream(ctx context.Context) delivery.MessageStream {
	fallback := o.fallback.StartStream(ctx)
	var foreground delivery.MessageStream
	return delivery.StreamFunc(func(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
		if o.execution.Foreground() == nil {
			return fallback.Send(ctx, event)
		}
		ctx = o.view.Context(ctx)
		if foreground == nil {
			foreground = o.foreground.StartStream(ctx)
		}
		return foreground.Send(ctx, event)
	})
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

func (o executionTurnOutput) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (operationresult.Result[delivery.Receipt], error) {
	ctx, out := o.target(ctx)
	return out.SendTo(ctx, target, outputs)
}
