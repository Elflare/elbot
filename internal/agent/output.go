package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/agent/dialogue"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/notification"
)

type outputSender struct {
	dispatcher    *dispatch.Router
	notifications *notification.Manager
	hooks         *hookBridge
	identity      *identityResolver
}

func (o *outputSender) SendOutputs(ctx context.Context, outputs []delivery.Output) error {
	_, err := o.dispatcher.SendNotice(ctx, delivery.Notice{Outputs: outputs})
	return err
}
func (o *outputSender) SendChat(ctx context.Context, text string) {
	_, _ = o.SendAssistant(ctx, text)
}

func (o *outputSender) SendNotice(ctx context.Context, notice delivery.Notice) error {
	_, err := o.dispatcher.SendNotice(ctx, notice)
	return err
}

func (o *outputSender) PrepareAssistant(ctx context.Context, point hook.Point, text string) (string, error) {
	event, err := o.hooks.Run(ctx, hook.Event{Point: point, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(text)}})
	if err != nil {
		return "", err
	}
	return llm.SegmentsTextOnly(event.Message.Segments), nil
}

func (o *outputSender) SendAssistant(ctx context.Context, text string) (delivery.Receipt, error) {
	if strings.TrimSpace(text) == "" && dialogue.BufferAssistantOutput(ctx) {
		return delivery.Receipt{}, nil
	}
	preparedText, err := o.PrepareAssistant(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return delivery.Receipt{}, err
	}
	receipt, err := o.dispatcher.SendChat(ctx, []delivery.Output{delivery.Text(preparedText)})
	if err != nil {

		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelWarn,
			Name:     "chat_send_failed",
			Module:   "agent",
			Summary:  "chat send failed",
			Fields:   []slog.Attr{slog.Any("error", err.Error())},
		})

		return receipt, err
	}
	o.hooks.Notify(ctx, hook.Event{Point: hook.PointPlatformMessageSent, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(preparedText)}})

	return receipt, nil
}

func (o *outputSender) TextNotice(ctx context.Context, level slog.Level, text string) {
	o.notifications.Text(ctx, level, text)
}

func (o *outputSender) Preview(ctx context.Context, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	event, err := o.hooks.Run(ctx, hook.Event{Point: hook.PointAgentOutputPrepared, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(text)}})
	if err != nil {
		return
	}
	body := strings.TrimSpace(llm.SegmentsTextOnly(event.Message.Segments))
	if body == "" {
		return
	}
	preview := formatToolPreview(body)
	o.dispatcher.SendNotice(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text(preview)}, Level: slog.LevelDebug})
	o.hooks.Notify(ctx, hook.Event{Point: hook.PointPlatformMessageSent, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(preview)}})
}

func formatToolPreview(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, line := range lines {
		lines[i] = "[tool] " + strings.TrimSpace(line)
	}
	return strings.Join(lines, "\n")
}

func (o *outputSender) FinishIntermediate(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error {
	if streaming {
		if strings.TrimSpace(text) != "" {
			if err := o.replaceStreamOutput(ctx, streamCtx, stream, text); err != nil {
				return err
			}
		}
		_, err := stream.Finish(streamCtx)
		return err
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if _, err := o.SendAssistant(ctx, text); err != nil {
		return err
	}
	return nil
}

func (o *outputSender) ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (delivery.Receipt, error) {
	prepared, err := o.PrepareAssistant(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return delivery.Receipt{}, err
	}
	receipt, err := stream.Replace(streamCtx, prepared)
	if err != nil {
		return receipt, fmt.Errorf("stream replace: %w", err)
	}
	finishReceipt, err := stream.Finish(streamCtx)
	if len(receipt.PlatformMessageIDs) == 0 {
		receipt = finishReceipt
	}
	return receipt, err
}

func (o *outputSender) replaceStreamOutput(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) error {
	prepared, err := o.PrepareAssistant(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return err
	}
	if _, err := stream.Replace(streamCtx, prepared); err != nil {
		return fmt.Errorf("stream replace: %w", err)
	}
	return nil
}

func (o *outputSender) StartStream(ctx context.Context) delivery.MessageStream {
	if dialogue.BufferAssistantOutput(ctx) {
		return nil
	}
	stream, err := o.dispatcher.StartStream(ctx)
	if err != nil {
		return nil
	}
	return stream
}

func (o *outputSender) Reasoning(ctx context.Context, text string) {
	if o.identity.IsCLI(ctx) && text != "" {
		_ = o.dispatcher.SendReasoning(ctx, text)
	}
}

// NotificationSender exposes the output component, never the Agent execution
// entrypoint. Notice consumers retain normal output Hooks and physical routing.
func (a *Agent) NotificationSender() interface {
	SendAssistant(context.Context, string) (delivery.Receipt, error)
} {
	return a.output
}
