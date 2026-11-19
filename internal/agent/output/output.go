package output

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/operationresult"
	"encoding/json"
	"log/slog"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
)

type OutputSender struct {
	dispatcher interface {
		SendTo(context.Context, delivery.Target, []delivery.Output) (operationresult.Result[delivery.Receipt], error)
		StartStream(context.Context) (operationresult.Result[delivery.MessageStream], error)
	}
	hooks interface {
		AgentOutputPrepared(context.Context, string) (string, error)
		AgentTurnOutputPrepared(context.Context, string) (string, error)
		PlatformMessageSent(context.Context, string)
	}
}

// Send delivers an independent message through the same event pipeline as a reply.
func (o *OutputSender) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	return o.StartStream(ctx).Send(ctx, event)
}

func (o *OutputSender) observeDelivery(ctx context.Context, operation string, receipt delivery.Receipt) {
	// Successful receipts are facts; callers own send failures.
	if len(receipt.SentMessages) == 0 && len(receipt.PlatformMessageIDs) == 0 {
		return
	}
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
	data, _ := json.Marshal(content)
	record := globalevents.LogRecord{
		Category:     globalevents.LogCategoryLog,
		Level:        slog.LevelDebug,
		EventName:    "reply_delivered",
		Module:       "agent",
		Type:         globalevents.ContentJSON,
		Content:      string(data),
		ResultStatus: globalevents.ResultSucceeded,
	}
	record.Identity = globalevents.LogIdentityFromContext(ctx)
	globalevents.Log(context.WithoutCancel(ctx), record)
}

func isBackground(ctx context.Context) bool {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	return facts.Mode == "background"
}

func (o *OutputSender) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (operationresult.Result[delivery.Receipt], error) {
	if target.Empty() && (isBackground(ctx)) {
		return operationresult.Result[delivery.Receipt]{Status: operationresult.Success}, nil
	}
	sent, err := o.dispatcher.SendTo(ctx, target, outputs)
	if err == nil && sent.Status == operationresult.Success {
		o.observeDelivery(ctx, "send_outputs", sent.Data)
	}
	return sent, err
}

func (o *OutputSender) PrepareAssistant(ctx context.Context, text string) (string, error) {
	prepared, err := o.hooks.AgentTurnOutputPrepared(ctx, text)
	if err != nil {
		return "", err
	}
	return prepared, nil
}

func (o *OutputSender) Notify(ctx context.Context, level slog.Level, text string) {
	if (isBackground(ctx)) || strings.TrimSpace(text) == "" {
		return
	}
	globalevents.Notify(ctx, globalevents.NewNotification(ctx, level, text))
}

// Preview treats each text argument as one complete tool-call notice, including
// any multiline body. General workflow notices also occupy one argument.
// It prefixes each nonblank notice once, joins them, and runs the output Hook once.
func (o *OutputSender) Preview(ctx context.Context, text ...string) {
	if isBackground(ctx) {
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
		return
	}
	o.Notify(ctx, slog.LevelDebug, strings.TrimSpace(prepared))
}

func formatToolPreview(text string) string {
	return "[tool] " + text
}

func (o *OutputSender) StartStream(ctx context.Context) delivery.MessageStream {
	var platformStream delivery.MessageStream
	closed := false
	return delivery.StreamFunc(func(ctx context.Context, event delivery.StreamEvent) (receipt delivery.Receipt, err error) {
		if closed || isBackground(ctx) {
			return receipt, nil
		}
		msg, _ := agentshare.MessageContextFrom(ctx)
		if msg.BufferAssistantOutput && event.Type != delivery.StreamSend && event.Type != delivery.StreamSendExtras && event.Type != delivery.StreamToolCalls && event.Type != delivery.StreamToolCall && event.Type != delivery.StreamToolResult {
			return receipt, nil
		}
		defer func() {
			if err != nil && (event.Type == delivery.StreamReasoningDelta || event.Type == delivery.StreamReasoningDone) {
				globalevents.AsyncLog(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelWarn, EventName: "reasoning_display_failed", Module: "agent", Content: err.Error(), Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.LogStatus(err)})
				err = nil
			}
		}()
		if event.Type == delivery.StreamSendExtras {
			if len(event.Outputs) == 0 {
				return receipt, nil
			}
			target, err := delivery.ValidateOutputsTarget(event.Outputs)
			if err != nil {
				return receipt, err
			}
			sent, err := o.dispatcher.SendTo(ctx, target, event.Outputs)
			if err != nil {
				return receipt, err
			}
			if sent.Status != operationresult.Success {
				o.Notify(ctx, slog.LevelWarn, sent.Message)
				return receipt, nil
			}
			o.observeDelivery(ctx, "send_outputs", sent.Data)
			return sent.Data, nil
		}
		// Text hooks cannot rewrite typed blocks without flattening their content.
		if event.Type == delivery.StreamSend && len(event.Blocks) == 0 {
			event.Text, err = o.hooks.AgentOutputPrepared(ctx, event.Text)
			if err != nil {
				return receipt, err
			}
		}
		if msg.BufferAssistantOutput && event.Type == delivery.StreamSend {
			sent, err := o.dispatcher.SendTo(ctx, delivery.Target{}, []delivery.Output{{Kind: delivery.KindText, Text: event.Text, Blocks: event.Blocks, Actions: event.Actions}})
			if err != nil {
				return receipt, err
			}
			if sent.Status != operationresult.Success {
				o.Notify(ctx, slog.LevelWarn, sent.Message)
				return receipt, nil
			}
			receipt = sent.Data
		} else {
			if platformStream == nil {
				opened, err := o.dispatcher.StartStream(ctx)
				if err != nil {
					return receipt, err
				}
				if opened.Status != operationresult.Success {
					closed = true
					o.Notify(ctx, slog.LevelWarn, opened.Message)
					return receipt, nil
				}
				platformStream = opened.Data
			}
			receipt, err = platformStream.Send(ctx, event)
			if err != nil {
				return receipt, err
			}
		}
		if event.Type == delivery.StreamSend {
			if len(event.Blocks) == 0 && strings.TrimSpace(event.Text) != "" {
				o.hooks.PlatformMessageSent(ctx, event.Text)
			}
			o.observeDelivery(ctx, "send_assistant_message", receipt)
		}
		return receipt, nil
	})
}

// NewOutputSender initializes OutputSender without installing cross-service callbacks.
func NewOutputSender(deps OutputSenderDeps) *OutputSender {
	return &OutputSender{
		dispatcher: deps.Dispatcher,
		hooks:      deps.Hooks,
	}
}
