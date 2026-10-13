package agent

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
)

type OutputSender struct {
	dispatcher *dispatch.Router
	hooks      dialogue.Hooks
	reasoning  func(context.Context) dialogue.ReasoningStream
}

func (o *OutputSender) SendOutputs(ctx context.Context, outputs []delivery.Output) (globalevents.LogResult, error) {
	_, logResult, err := o.dispatcher.SendTo(ctx, delivery.Target{}, outputs)
	if len(outputs) > 0 || err != nil {
		o.observeDelivery(ctx, "send_outputs", logResult)
	}
	return logResult, err
}

func (o *OutputSender) observeDelivery(ctx context.Context, operation string, logResult globalevents.LogResult) {
	// This source records only the send operation, never the overall tool result.
	record := globalevents.LogRecord{
		Category:     globalevents.LogRuntime,
		Level:        slog.LevelDebug,
		EventName:    "reply_delivered",
		Module:       "agent",
		Type:         globalevents.ContentText,
		ContentLimit: 4096,
		Content:      "operation=" + operation,
		ResultStatus: logResult.ResultStatus,
	}
	if logResult.Content != "" {
		record.Content += "\n" + logResult.Content
	}
	if logResult.ResultStatus == globalevents.ResultFailed {
		record.Level = slog.LevelError
	} else if logResult.ResultStatus == globalevents.ResultCanceled {
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
	_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
}

func (o *OutputSender) SendChat(ctx context.Context, text string) {
	_, _, _ = o.SendAssistant(ctx, text)
}

func (o *OutputSender) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) error {
	_, _, err := o.dispatcher.SendTo(ctx, target, outputs)
	return err
}

func (o *OutputSender) PrepareAssistant(ctx context.Context, point hook.Point, text string) (string, globalevents.LogResult, error) {
	event, logResult, err := o.hooks.Run(ctx, hook.Event{Point: point, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(text)}})
	if err != nil {
		return "", logResult, err
	}
	return llm.SegmentsTextOnly(event.Message.Segments), logResult, nil
}

func (o *OutputSender) SendAssistant(ctx context.Context, text string) (receipt delivery.Receipt, logResult globalevents.LogResult, err error) {
	if strings.TrimSpace(text) == "" && dialogue.BufferAssistantOutput(ctx) {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "assistant output is buffered and empty"}, nil
	}
	defer func() { o.observeDelivery(ctx, "send_assistant_message", logResult) }()
	preparedText, logResult, err := o.PrepareAssistant(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return delivery.Receipt{}, logResult, err
	}
	receipt, logResult, err = o.dispatcher.SendChat(ctx, []delivery.Output{delivery.Text(preparedText)})
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

func (o *OutputSender) Notify(ctx context.Context, level slog.Level, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	_, _ = globalevents.NotificationRequested.EmitAsync(ctx, globalevents.NewNotification(ctx, level, text))
}

// Preview treats each text argument as one complete tool-call notice, including
// any multiline body. General workflow notices also occupy one argument.
// It prefixes each nonblank notice once, joins them, and runs the output Hook once.
func (o *OutputSender) Preview(ctx context.Context, text ...string) {
	lines := make([]string, 0, len(text))
	for _, line := range text {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, formatToolPreview(line))
	}
	if len(lines) == 0 {
		return
	}
	preview := strings.Join(lines, "\n")
	event, _, err := o.hooks.Run(ctx, hook.Event{Point: hook.PointAgentOutputPrepared, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(preview)}})
	if err != nil {
		return
	}
	body := strings.TrimSpace(llm.SegmentsTextOnly(event.Message.Segments))
	if body == "" {
		return
	}
	o.Notify(ctx, slog.LevelDebug, body)
}

func formatToolPreview(text string) string {
	return "[tool] " + text
}

func (o *OutputSender) FinishIntermediate(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error {
	if streaming {
		if strings.TrimSpace(text) != "" {
			_, _, err := o.ReplaceAndFinishStream(ctx, streamCtx, stream, text)
			return err
		}
		receipt, logResult, err := stream.Finish(streamCtx)
		if logResult.Content != "" {
			logResult.Content += "\n"
		}
		logResult.Content += fmt.Sprintf("platform_message_ids=%v", receipt.PlatformMessageIDs)
		for _, sent := range receipt.SentMessages {
			logResult.Content += fmt.Sprintf("\nsent_message: platform=%s scope_id=%s platform_message_id=%s output_indexes=%v", sent.Platform, sent.ScopeID, sent.PlatformMessageID, sent.OutputIndexes)
		}
		o.observeDelivery(ctx, "send_assistant_message", logResult)
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

func (o *OutputSender) ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (receipt delivery.Receipt, logResult globalevents.LogResult, err error) {
	defer func() {
		if logResult.Content != "" {
			logResult.Content += "\n"
		}
		logResult.Content += fmt.Sprintf("platform_message_ids=%v", receipt.PlatformMessageIDs)
		for _, sent := range receipt.SentMessages {
			logResult.Content += fmt.Sprintf("\nsent_message: platform=%s scope_id=%s platform_message_id=%s output_indexes=%v", sent.Platform, sent.ScopeID, sent.PlatformMessageID, sent.OutputIndexes)
		}
		o.observeDelivery(ctx, "send_assistant_message", logResult)
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
	if logResult.Content != "" {
		logResult.Content = "finish:\n" + logResult.Content
	}
	if result.Content != "" {
		if logResult.Content != "" {
			logResult.Content = "\n" + logResult.Content
		}
		logResult.Content = "replace:\n" + result.Content + logResult.Content
	}
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

func (o *OutputSender) StartStream(ctx context.Context) delivery.MessageStream {
	if dialogue.BufferAssistantOutput(ctx) {
		return nil
	}
	stream, err := o.dispatcher.StartStream(ctx)
	if err != nil {
		return nil
	}
	return stream
}

func (o *OutputSender) StartReasoning(ctx context.Context) dialogue.ReasoningStream {
	if o.reasoning == nil {
		return nil
	}
	return o.reasoning(ctx)
}

// NewOutputSender initializes OutputSender without installing cross-service callbacks.
func NewOutputSender(deps OutputSenderDeps) (*OutputSender, error) {
	if deps.Dispatcher == nil {
		return nil, fmt.Errorf("OutputSender dispatcher is required")
	}
	if deps.Hooks == nil {
		return nil, fmt.Errorf("OutputSender hooks is required")
	}
	return &OutputSender{
		dispatcher: deps.Dispatcher,
		hooks:      deps.Hooks,
		reasoning:  deps.Reasoning,
	}, nil
}
