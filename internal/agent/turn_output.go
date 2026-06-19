package agent

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
)

type turnOutput interface {
	StartStream(ctx context.Context) delivery.MessageStream
	FinishIntermediate(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error
	ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (delivery.Receipt, error)
	SendAssistant(ctx context.Context, text string) (delivery.Receipt, error)
	SendOutputs(ctx context.Context, outputs []delivery.Output) error
	SendPreview(ctx context.Context, text string)
	SendReasoning(ctx context.Context, text string)
	PublishRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot)
}

type foregroundTurnOutput struct{ agent *Agent }

type backgroundTurnOutput struct{ agent *Agent }

func (o foregroundTurnOutput) StartStream(ctx context.Context) delivery.MessageStream {
	return o.agent.startMessageStream(ctx)
}

func (o foregroundTurnOutput) FinishIntermediate(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error {
	return o.agent.finishIntermediateOutput(ctx, streamCtx, stream, text, streaming)
}

func (o foregroundTurnOutput) ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (delivery.Receipt, error) {
	return o.agent.replaceAndFinishStream(ctx, streamCtx, stream, text)
}

func (o foregroundTurnOutput) SendAssistant(ctx context.Context, text string) (delivery.Receipt, error) {
	return o.agent.sendChatWithReceipt(ctx, text)
}

func (o foregroundTurnOutput) SendOutputs(ctx context.Context, outputs []delivery.Output) error {
	return o.agent.sendOutputs(ctx, outputs)
}

func (o foregroundTurnOutput) SendPreview(ctx context.Context, text string) {
	o.agent.sendPreview(ctx, text)
}

func (o foregroundTurnOutput) SendReasoning(ctx context.Context, text string) {
	o.agent.sendCLIReasoning(ctx, text)
}

func (o foregroundTurnOutput) PublishRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) {
	o.agent.publishRuntimeStatus(ctx, snapshot)
}

func (o backgroundTurnOutput) StartStream(ctx context.Context) delivery.MessageStream { return nil }

func (o backgroundTurnOutput) FinishIntermediate(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error {
	return nil
}

func (o backgroundTurnOutput) ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func (o backgroundTurnOutput) SendAssistant(ctx context.Context, text string) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func (o backgroundTurnOutput) SendOutputs(ctx context.Context, outputs []delivery.Output) error {
	return nil
}

func (o backgroundTurnOutput) SendPreview(ctx context.Context, text string) {}

func (o backgroundTurnOutput) SendReasoning(ctx context.Context, text string) {}

func (o backgroundTurnOutput) PublishRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) {
	o.agent.recordRuntimeStatus(snapshot)
}

func (a *Agent) sendPreview(ctx context.Context, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	event, err := a.runHook(ctx, hook.Event{Point: hook.PointAgentOutputPrepared, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(text)}})
	if err != nil {
		return
	}
	body := strings.TrimSpace(llm.SegmentsTextOnly(event.Message.Segments))
	if body == "" {
		return
	}
	preview := "[tool] " + body
	_ = a.sendNoticeOutput(ctx, delivery.Target{}, delivery.Text(preview))
	a.notifyHook(ctx, hook.Event{Point: hook.PointPlatformMessageSent, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(preview)}})
}

func (a *Agent) finishIntermediateOutput(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error {
	if streaming {
		if strings.TrimSpace(text) != "" {
			if err := a.replaceStreamOutput(ctx, streamCtx, stream, text); err != nil {
				return err
			}
		}
		_, err := stream.Finish(streamCtx)
		return err
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if _, err := a.sendChatWithReceipt(ctx, text); err != nil {
		return err
	}
	return nil
}

func (a *Agent) replaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (delivery.Receipt, error) {
	prepared, err := a.prepareAssistantOutput(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return delivery.Receipt{}, err
	}
	receipt, err := stream.Replace(streamCtx, prepared)
	if err != nil {
		return delivery.Receipt{}, fmt.Errorf("stream replace: %w", err)
	}
	finishReceipt, err := stream.Finish(streamCtx)
	if err != nil {
		return delivery.Receipt{}, err
	}
	if len(receipt.PlatformMessageIDs) == 0 {
		receipt = finishReceipt
	}
	return receipt, nil
}

func (a *Agent) replaceStreamOutput(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) error {
	prepared, err := a.prepareAssistantOutput(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return err
	}
	if _, err := stream.Replace(streamCtx, prepared); err != nil {
		return fmt.Errorf("stream replace: %w", err)
	}
	return nil
}

func (a *Agent) startMessageStream(ctx context.Context) delivery.MessageStream {
	if bufferAssistantOutput(ctx) {
		return nil
	}
	if msg, ok := platform.MessageContextFrom(ctx); ok && msg.Sender != nil {
		if sender, ok := msg.Sender.(delivery.StreamingMessageSender); ok {
			stream, err := sender.StartStream(ctx)
			if err == nil {
				return stream
			}
		}
	}
	if sender, ok := a.platform.(delivery.StreamingMessageSender); ok {
		stream, err := sender.StartStream(ctx)
		if err == nil {
			return stream
		}
	}
	return nil
}
