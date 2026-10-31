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
		SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error)
		SendChat(context.Context, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error)
		StartStream(context.Context) (delivery.MessageStream, error)
	}
	hooks interface {
		AgentOutputPrepared(context.Context, string) (string, globalevents.LogResult, error)
		AgentTurnOutputPrepared(context.Context, string) (string, globalevents.LogResult, error)
		PlatformMessageSent(context.Context, string)
	}
	reasoning func(context.Context) delivery.ReasoningStream
}

func (o *OutputSender) SendOutputs(ctx context.Context, outputs []delivery.Output) (globalevents.LogResult, error) {
	if contextinfo.BackgroundContext(ctx) {
		return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "background output is silent"}, nil
	}
	receipt, logResult, err := o.dispatcher.SendTo(ctx, delivery.Target{}, outputs)
	if len(outputs) > 0 || err != nil {
		o.observeDelivery(ctx, "send_outputs", receipt, logResult)
	}
	return logResult, err
}

func (o *OutputSender) observeDelivery(ctx context.Context, operation string, receipt delivery.Receipt, logResult globalevents.LogResult) {
	// This source records only the send operation, never the overall tool result.
	sentMessages := make([]map[string]any, 0, len(receipt.SentMessages))
	for _, sent := range receipt.SentMessages {
		sentMessages = append(sentMessages, map[string]any{
			"platform": sent.Platform, "scope_id": sent.ScopeID,
			"platform_message_id": sent.PlatformMessageID, "output_indexes": sent.OutputIndexes,
		})
	}
	content := map[string]any{
		"operation": operation, "platform_message_ids": receipt.PlatformMessageIDs, "sent_messages": sentMessages,
	}
	if logResult.Content != "" {
		content["detail"] = agentshare.TruncateLogContent(logResult.Content, 4096)
	}
	data, _ := json.Marshal(content)
	record := globalevents.LogRecord{
		Category:     globalevents.LogRuntime,
		Level:        slog.LevelDebug,
		EventName:    "reply_delivered",
		Module:       "agent",
		Type:         globalevents.ContentJSON,
		Content:      string(data),
		ResultStatus: logResult.ResultStatus,
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

func (o *OutputSender) SendChat(ctx context.Context, text string) error {
	_, _, err := o.SendAssistant(ctx, text)
	return err
}

func (o *OutputSender) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) error {
	if target.Empty() && contextinfo.BackgroundContext(ctx) {
		return nil
	}
	_, _, err := o.dispatcher.SendTo(ctx, target, outputs)
	return err
}

func (o *OutputSender) PrepareAssistant(ctx context.Context, text string) (string, globalevents.LogResult, error) {
	prepared, logResult, err := o.hooks.AgentTurnOutputPrepared(ctx, text)
	if err != nil {
		return "", logResult, err
	}
	return prepared, logResult, nil
}

func (o *OutputSender) SendAssistant(ctx context.Context, text string, actions ...delivery.Action) (receipt delivery.Receipt, logResult globalevents.LogResult, err error) {
	if contextinfo.BackgroundContext(ctx) {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "background output is silent"}, nil
	}
	if strings.TrimSpace(text) == "" && agentshare.BufferAssistantOutput(ctx) {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "assistant output is buffered and empty"}, nil
	}
	defer func() { o.observeDelivery(ctx, "send_assistant_message", receipt, logResult) }()
	preparedText, logResult, err := o.hooks.AgentOutputPrepared(ctx, text)
	if err != nil {
		return delivery.Receipt{}, logResult, err
	}
	// Hook 只改正文，操作由业务源提供，与展示文本独立。
	receipt, logResult, err = o.dispatcher.SendChat(ctx, []delivery.Output{{Kind: delivery.KindText, Text: preparedText, Actions: slices.Clone(actions)}})
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
	o.hooks.PlatformMessageSent(ctx, preparedText)

	return receipt, logResult, nil
}

func (o *OutputSender) Notify(ctx context.Context, level slog.Level, text string) {
	if contextinfo.BackgroundContext(ctx) || strings.TrimSpace(text) == "" {
		return
	}
	_, _ = globalevents.NotificationRequested.EmitAsync(ctx, globalevents.NewNotification(ctx, level, text))
}

// Preview treats each text argument as one complete tool-call notice, including
// any multiline body. General workflow notices also occupy one argument.
// It prefixes each nonblank notice once, joins them, and runs the output Hook once.
func (o *OutputSender) Preview(ctx context.Context, text ...string) {
	if contextinfo.BackgroundContext(ctx) {
		return
	}
	info, _ := contextinfo.ConversationFromContext(ctx)
	if info.Source.ConversationKind == contextinfo.ConversationGroup {
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
	prepared, _, err := o.hooks.AgentOutputPrepared(ctx, preview)
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
			_, _, err := o.ReplaceAndFinishStream(ctx, streamCtx, stream, text)
			return err
		}
		receipt, logResult, err := stream.Finish(streamCtx)
		o.observeDelivery(ctx, "send_assistant_message", receipt, logResult)
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
	defer func() { o.observeDelivery(ctx, "send_assistant_message", receipt, logResult) }()
	prepared, logResult, err := o.hooks.AgentOutputPrepared(ctx, text)
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

func (o *OutputSender) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	if contextinfo.BackgroundContext(ctx) || agentshare.BufferAssistantOutput(ctx) {
		return nil, nil
	}
	return o.dispatcher.StartStream(ctx)
}

func (o *OutputSender) StartReasoning(ctx context.Context) delivery.ReasoningStream {
	if contextinfo.BackgroundContext(ctx) || o.reasoning == nil {
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
