package output

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
)

type OutputSender struct {
	dispatcher interface {
		SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)
		StartStream(context.Context) (delivery.MessageStream, error)
	}
	hooks interface {
		AgentOutputPrepared(context.Context, string) (string, error)
		AgentTurnOutputPrepared(context.Context, string) (string, error)
		PlatformMessageSent(context.Context, string)
	}
	reasoning func(context.Context) delivery.ReasoningStream
}

func (o *OutputSender) SendOutputs(ctx context.Context, outputs []delivery.Output) error {
	if agentshare.ExecutionMode(ctx) == "background" {
		return nil
	}
	target, err := delivery.ValidateOutputsTarget(outputs)
	var receipt delivery.Receipt
	if err == nil {
		receipt, err = o.dispatcher.SendTo(ctx, target, outputs)
	}
	if len(outputs) > 0 || err != nil {
		o.observeDelivery(ctx, "send_outputs", receipt, err)
	}
	return err
}

func (o *OutputSender) observeDelivery(ctx context.Context, operation string, receipt delivery.Receipt, err error) {
	// This source records only the send operation, never the overall tool result.
	sentMessages := make([]map[string]any, 0, len(receipt.SentMessages))
	for _, sent := range receipt.SentMessages {
		sentMessages = append(sentMessages, map[string]any{
			"platform": sent.Platform, "chat_type": sent.ChatType, "chat_id": sent.ChatID,
			"platform_message_id": sent.PlatformMessageID, "output_indexes": sent.OutputIndexes,
		})
	}
	content := map[string]any{
		"operation": operation, "platform_message_ids": receipt.PlatformMessageIDs, "sent_messages": sentMessages,
	}
	if err != nil {
		content["detail"] = agentshare.TruncateLogContent(err.Error(), 4096)
	}
	data, _ := json.Marshal(content)
	record := globalevents.LogRecord{
		Category:     globalevents.LogRuntime,
		Level:        slog.LevelDebug,
		EventName:    "reply_delivered",
		Module:       "agent",
		Type:         globalevents.ContentJSON,
		Content:      string(data),
		ResultStatus: globalevents.ResultSucceeded,
	}
	if err != nil {
		record.Level, record.ResultStatus = slog.LevelWarn, globalevents.ResultFailed
		if globalevents.LogStatus(err) == globalevents.ResultCanceled {
			record.Level, record.ResultStatus = slog.LevelInfo, globalevents.ResultCanceled
		}
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record.Identity = globalevents.LogIdentityFromExecution(facts)
	_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
}

func (o *OutputSender) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) error {
	if target.Empty() && (agentshare.ExecutionMode(ctx) == "background") {
		return nil
	}
	_, err := o.dispatcher.SendTo(ctx, target, outputs)
	return err
}

func (o *OutputSender) PrepareAssistant(ctx context.Context, text string) (string, error) {
	prepared, err := o.hooks.AgentTurnOutputPrepared(ctx, text)
	if err != nil {
		return "", err
	}
	return prepared, nil
}

func (o *OutputSender) SendAssistant(ctx context.Context, text string, actions ...delivery.Action) (receipt delivery.Receipt, err error) {
	if agentshare.ExecutionMode(ctx) == "background" {
		return delivery.Receipt{}, nil
	}
	if strings.TrimSpace(text) == "" && agentshare.BufferAssistantOutput(ctx) {
		return delivery.Receipt{}, nil
	}
	defer func() { o.observeDelivery(ctx, "send_assistant_message", receipt, err) }()
	preparedText, err := o.hooks.AgentOutputPrepared(ctx, text)
	if err != nil {
		return delivery.Receipt{}, err
	}
	// Hook 只改正文，操作由业务源提供，与展示文本独立。
	receipt, err = o.dispatcher.SendTo(ctx, delivery.Target{}, []delivery.Output{{Kind: delivery.KindText, Text: preparedText, Actions: slices.Clone(actions)}})
	if err != nil {
		return receipt, err
	}
	o.hooks.PlatformMessageSent(ctx, preparedText)

	return receipt, nil
}

func (o *OutputSender) Notify(ctx context.Context, level slog.Level, text string) {
	if (agentshare.ExecutionMode(ctx) == "background") || strings.TrimSpace(text) == "" {
		return
	}
	_, _ = globalevents.NotificationRequested.EmitAsync(ctx, globalevents.NewNotification(ctx, level, text))
}

// Preview treats each text argument as one complete tool-call notice, including
// any multiline body. General workflow notices also occupy one argument.
// It prefixes each nonblank notice once, joins them, and runs the output Hook once.
func (o *OutputSender) Preview(ctx context.Context, text ...string) {
	if agentshare.ExecutionMode(ctx) == "background" {
		return
	}
	info, _ := contextinfo.ConversationFromContext(ctx)
	if info.Source.ChatType == contextinfo.ChatGroup {
		return
	}
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
	prepared, err := o.hooks.AgentOutputPrepared(ctx, preview)
	if err != nil {
		if ctx.Err() == nil {
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			agentshare.ReportFailure(ctx, facts.SessionID, fmt.Errorf("准备工具预览: %w", err), o.Notify)
		}
		return
	}
	body := strings.TrimSpace(prepared)
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
			_, err := o.ReplaceAndFinishStream(ctx, streamCtx, stream, text)
			return err
		}
		receipt, err := stream.Finish(streamCtx)
		o.observeDelivery(ctx, "send_assistant_message", receipt, err)
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

func (o *OutputSender) ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (receipt delivery.Receipt, err error) {
	defer func() { o.observeDelivery(ctx, "send_assistant_message", receipt, err) }()
	prepared, err := o.hooks.AgentOutputPrepared(ctx, text)
	if err != nil {
		return delivery.Receipt{}, err
	}
	receipt, err = stream.Replace(streamCtx, prepared)
	if err != nil {
		return receipt, fmt.Errorf("stream replace: %w", err)
	}
	finishReceipt, err := stream.Finish(streamCtx)
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
			return message.Platform == sent.Platform && message.ChatType == sent.ChatType && message.ChatID == sent.ChatID && message.PlatformMessageID == sent.PlatformMessageID
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
	return receipt, err
}

func (o *OutputSender) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	if (agentshare.ExecutionMode(ctx) == "background") || agentshare.BufferAssistantOutput(ctx) {
		return nil, nil
	}
	return o.dispatcher.StartStream(ctx)
}

func (o *OutputSender) StartReasoning(ctx context.Context) delivery.ReasoningStream {
	if (agentshare.ExecutionMode(ctx) == "background") || o.reasoning == nil {
		return nil
	}
	return o.reasoning(ctx)
}

// NewOutputSender initializes OutputSender without installing cross-service callbacks.
func NewOutputSender(deps OutputSenderDeps) *OutputSender {
	return &OutputSender{
		dispatcher: deps.Dispatcher,
		hooks:      deps.Hooks,
		reasoning:  deps.Reasoning,
	}
}
