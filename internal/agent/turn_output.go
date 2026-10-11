package agent

import (
	"context"
	"log/slog"

	"elbot/internal/delivery"
	"elbot/internal/events"
	runtimestatus "elbot/internal/runtime"
)

type foregroundTurnOutput struct {
	sender ExecutionSender
	status ExecutionStatus
}

type backgroundTurnOutput struct{ status ExecutionStatus }

func (o foregroundTurnOutput) StartStream(ctx context.Context) delivery.MessageStream {
	return o.sender.StartStream(ctx)
}

func (o foregroundTurnOutput) FinishIntermediate(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error {
	return o.sender.FinishIntermediate(ctx, streamCtx, stream, text, streaming)
}

func (o foregroundTurnOutput) ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (delivery.Receipt, events.LogResult, error) {
	return o.sender.ReplaceAndFinishStream(ctx, streamCtx, stream, text)
}

func (o foregroundTurnOutput) SendAssistant(ctx context.Context, text string) (delivery.Receipt, events.LogResult, error) {
	return o.sender.SendAssistant(ctx, text)
}

func (o foregroundTurnOutput) SendOutputs(ctx context.Context, outputs []delivery.Output) (events.LogResult, error) {
	return o.sender.SendOutputs(ctx, outputs)
}

func (o foregroundTurnOutput) SendNotice(ctx context.Context, level slog.Level, text string) {
	o.sender.TextNotice(ctx, level, text)
}

func (o foregroundTurnOutput) SendPreview(ctx context.Context, text string) {
	o.sender.Preview(ctx, text)
}

func (o foregroundTurnOutput) SendReasoning(ctx context.Context, text string) {
	o.sender.Reasoning(ctx, text)
}

func (o foregroundTurnOutput) PublishRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) {
	o.status.Record(ctx, snapshot, true)
}

func (o backgroundTurnOutput) StartStream(ctx context.Context) delivery.MessageStream { return nil }

func (o backgroundTurnOutput) FinishIntermediate(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error {
	return nil
}

func (o backgroundTurnOutput) ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (delivery.Receipt, events.LogResult, error) {
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSkipped, Content: "background output is buffered"}, nil
}

func (o backgroundTurnOutput) SendAssistant(ctx context.Context, text string) (delivery.Receipt, events.LogResult, error) {
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSkipped, Content: "background output is buffered"}, nil
}

func (o backgroundTurnOutput) SendOutputs(ctx context.Context, outputs []delivery.Output) (events.LogResult, error) {
	return events.LogResult{ResultStatus: events.ResultSkipped, Content: "background output is buffered"}, nil
}

func (o backgroundTurnOutput) SendNotice(ctx context.Context, level slog.Level, text string) {}

func (o backgroundTurnOutput) SendPreview(ctx context.Context, text string) {}

func (o backgroundTurnOutput) SendReasoning(ctx context.Context, text string) {}

func (o backgroundTurnOutput) PublishRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) {
	o.status.Record(ctx, snapshot, false)
}
