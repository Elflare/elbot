package agent

import (
	"context"
	"elbot/internal/contextinfo"
	"fmt"
	"log/slog"
	"slices"
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

func (o *outputSender) SendOutputs(ctx context.Context, outputs []delivery.Output) (globalevents.LogResult, error) {
	_, logResult, err := o.dispatcher.SendNotice(ctx, delivery.Notice{Outputs: outputs})
	if len(outputs) > 0 || err != nil {
		// This source records only the send operation, never the overall tool result.
		record := globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelInfo, EventName: "reply_delivered", Module: "agent", Type: globalevents.ContentText, ContentLimit: 4096, Content: "operation=send_outputs\n" + logResult.Content, ResultStatus: logResult.ResultStatus}
		if logResult.ResultStatus == globalevents.ResultFailed {
			record.Level = slog.LevelError
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		if facts.SessionID != "" {
			record.Identity.SessionID = &facts.SessionID
		}
		if facts.RunID != "" {
			record.Identity.RunID = &facts.RunID
		}
		if facts.Attempt != "" {
			record.Identity.Attempt = &facts.Attempt
		}
		if facts.RequestID != "" {
			record.Identity.RequestID = &facts.RequestID
		}
		if facts.RootRequestID != "" {
			record.Identity.RootRequestID = &facts.RootRequestID
		}
		_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
	}
	return logResult, err
}
func (o *outputSender) SendChat(ctx context.Context, text string) {
	_, _, _ = o.SendAssistant(ctx, text)
}

func (o *outputSender) SendNotice(ctx context.Context, notice delivery.Notice) error {
	_, _, err := o.dispatcher.SendNotice(ctx, notice)
	return err
}

func (o *outputSender) PrepareAssistant(ctx context.Context, point hook.Point, text string) (string, globalevents.LogResult, error) {
	event, logResult, err := o.hooks.Run(ctx, hook.Event{Point: point, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(text)}})
	if err != nil {
		return "", logResult, err
	}
	return llm.SegmentsTextOnly(event.Message.Segments), logResult, nil
}

func (o *outputSender) SendAssistant(ctx context.Context, text string) (delivery.Receipt, globalevents.LogResult, error) {
	if strings.TrimSpace(text) == "" && dialogue.BufferAssistantOutput(ctx) {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "assistant output is buffered and empty"}, nil
	}
	preparedText, logResult, err := o.PrepareAssistant(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return delivery.Receipt{}, logResult, err
	}
	receipt, logResult, err := o.dispatcher.SendChat(ctx, []delivery.Output{delivery.Text(preparedText)})
	if logResult.ResultStatus == globalevents.ResultFailed || logResult.ResultStatus == globalevents.ResultCanceled {

		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "chat_send_failed",
				Module:       "agent",
				Type:         globalevents.ContentText,
				Content:      logResult.Content,
				ContentLimit: 4096,
				ResultStatus: logResult.ResultStatus,
			}
			if logResult.ResultStatus == globalevents.ResultCanceled {
				record.Level = slog.LevelInfo
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			_ = globalevents.EmitLog(ctx, record)
		}

	}
	if err != nil {
		return receipt, logResult, err
	}
	o.hooks.Notify(ctx, hook.Event{Point: hook.PointPlatformMessageSent, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(preparedText)}})

	return receipt, logResult, nil
}

func (o *outputSender) TextNotice(ctx context.Context, level slog.Level, text string) {
	o.notifications.Text(ctx, level, text)
}

func (o *outputSender) Preview(ctx context.Context, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	event, _, err := o.hooks.Run(ctx, hook.Event{Point: hook.PointAgentOutputPrepared, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(text)}})
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
		_, _, err := stream.Finish(streamCtx)
		return err
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if _, _, err := o.SendAssistant(ctx, text); err != nil {
		return err
	}
	return nil
}

func (o *outputSender) ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (receipt delivery.Receipt, logResult globalevents.LogResult, err error) {
	defer func() {
		logResult.Content += fmt.Sprintf("\nplatform_message_ids=%v", receipt.PlatformMessageIDs)
		for _, sent := range receipt.SentMessages {
			logResult.Content += fmt.Sprintf("\nsent_message: platform=%s scope_id=%s platform_message_id=%s output_indexes=%v", sent.Platform, sent.ScopeID, sent.PlatformMessageID, sent.OutputIndexes)
		}
	}()
	prepared, logResult, err := o.PrepareAssistant(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return delivery.Receipt{}, logResult, err
	}
	receipt, logResult, err = stream.Replace(streamCtx, prepared)
	if err != nil {
		return receipt, logResult, fmt.Errorf("stream replace: %w", err)
	}
	result := logResult
	finishReceipt, logResult, err := stream.Finish(streamCtx)
	logResult.Content = "replace:\n" + result.Content + "\nfinish:\n" + logResult.Content
	if result.ResultStatus == globalevents.ResultFailed || (result.ResultStatus == globalevents.ResultCanceled && logResult.ResultStatus != globalevents.ResultFailed) {
		logResult.ResultStatus = result.ResultStatus
	}
	receipt.PlatformMessageIDs = append([]string(nil), receipt.PlatformMessageIDs...)
	receipt.SentMessages = append([]delivery.SentMessage(nil), receipt.SentMessages...)
	for i := range receipt.SentMessages {
		receipt.SentMessages[i].OutputIndexes = append([]int(nil), receipt.SentMessages[i].OutputIndexes...)
	}
	for _, id := range finishReceipt.PlatformMessageIDs {
		if !slices.Contains(receipt.PlatformMessageIDs, id) {
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, id)
		}
	}
	for _, sent := range finishReceipt.SentMessages {
		i := slices.IndexFunc(receipt.SentMessages, func(message delivery.SentMessage) bool {
			return message.Platform == sent.Platform && message.ScopeID == sent.ScopeID && message.PlatformMessageID == sent.PlatformMessageID
		})
		if i < 0 {
			sent.OutputIndexes = append([]int(nil), sent.OutputIndexes...)
			receipt.SentMessages = append(receipt.SentMessages, sent)
			continue
		}
		for _, index := range sent.OutputIndexes {
			if !slices.Contains(receipt.SentMessages[i].OutputIndexes, index) {
				receipt.SentMessages[i].OutputIndexes = append(receipt.SentMessages[i].OutputIndexes, index)
			}
		}
	}
	return receipt, logResult, err
}

func (o *outputSender) replaceStreamOutput(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) error {
	prepared, _, err := o.PrepareAssistant(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return err
	}
	if _, _, err := stream.Replace(streamCtx, prepared); err != nil {
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
	SendAssistant(context.Context, string) (delivery.Receipt, globalevents.LogResult, error)
} {
	return a.output
}
