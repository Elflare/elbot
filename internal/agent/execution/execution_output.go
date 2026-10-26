package execution

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"log/slog"

	"elbot/internal/delivery"
	"elbot/internal/events"

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
	ctx, out := o.target(ctx)
	return out.StartStream(ctx)
}

func (o executionTurnOutput) FinishIntermediate(ctx, streamCtx context.Context, s delivery.MessageStream, text string, streaming bool) error {
	ctx, out := o.target(ctx)
	return out.FinishIntermediate(ctx, o.view.Context(streamCtx), s, text, streaming)
}

func (o executionTurnOutput) ReplaceAndFinishStream(ctx, streamCtx context.Context, s delivery.MessageStream, text string) (delivery.Receipt, events.LogResult, error) {
	ctx, out := o.target(ctx)
	return out.ReplaceAndFinishStream(ctx, o.view.Context(streamCtx), s, text)
}

func (o executionTurnOutput) SendAssistant(ctx context.Context, text string) (delivery.Receipt, events.LogResult, error) {
	ctx, out := o.target(ctx)
	return out.SendAssistant(ctx, text)
}

func (o executionTurnOutput) SendOutputs(ctx context.Context, v []delivery.Output) (events.LogResult, error) {
	ctx, out := o.target(ctx)
	return out.SendOutputs(ctx, v)
}

func (o executionTurnOutput) Notify(ctx context.Context, l slog.Level, text string) {
	ctx, out := o.target(ctx)
	out.Notify(ctx, l, text)
}

func (o executionTurnOutput) SendPreview(ctx context.Context, text ...string) {
	ctx, out := o.target(ctx)
	out.SendPreview(ctx, text...)
}

func (o executionTurnOutput) StartReasoning(ctx context.Context) delivery.ReasoningStream {
	stream := o.foreground.StartReasoning(ctx)
	if stream == nil {
		return nil
	}
	return executionReasoningStream{view: o.view, stream: stream}
}

// Resolve foreground context for each new delta, including adoption mid-call.
type executionReasoningStream struct {
	view interface {
		Context(context.Context) context.Context
	}
	stream delivery.ReasoningStream
}

func (s executionReasoningStream) Append(ctx context.Context, text string) {
	s.stream.Append(s.view.Context(ctx), text)
}

func (s executionReasoningStream) Finish(ctx context.Context) { s.stream.Finish(ctx) }
