package output

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"encoding/json"
	"fmt"
	"log/slog"
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
}

// Send delivers an independent message through the same event pipeline as a reply.
func (o *OutputSender) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	return o.StartStream(ctx).Send(ctx, event)
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
	globalevents.Log(context.WithoutCancel(ctx), record)
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

func (o *OutputSender) Notify(ctx context.Context, level slog.Level, text string) {
	if (agentshare.ExecutionMode(ctx) == "background") || strings.TrimSpace(text) == "" {
		return
	}
	globalevents.Notify(ctx, globalevents.NewNotification(ctx, level, text))
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
	o.Notify(ctx, slog.LevelDebug, strings.TrimSpace(prepared))
}

func formatToolPreview(text string) string {
	return "[tool] " + text
}

func (o *OutputSender) StartStream(ctx context.Context) delivery.MessageStream {
	var platformStream delivery.MessageStream
	return delivery.StreamFunc(func(ctx context.Context, event delivery.StreamEvent) (receipt delivery.Receipt, err error) {
		if agentshare.ExecutionMode(ctx) == "background" {
			return receipt, nil
		}
		buffered := agentshare.BufferAssistantOutput(ctx)
		if buffered && event.Type != delivery.StreamSend && event.Type != delivery.StreamSendExtras {
			return receipt, nil
		}
		defer func() {
			if err != nil && event.Type != delivery.StreamSend && event.Type != delivery.StreamSendExtras {
				slog.WarnContext(context.WithoutCancel(ctx), "message stream display failed", "event", event.Type, "error", err)
				if event.Type == delivery.StreamReasoningDelta || event.Type == delivery.StreamReasoningDone {
					err = nil
				}
			}
		}()
		switch event.Type {
		case delivery.StreamSendExtras:
			if len(event.Outputs) == 0 {
				return receipt, nil
			}
			defer func() { o.observeDelivery(ctx, "send_outputs", receipt, err) }()
			target, err := delivery.ValidateOutputsTarget(event.Outputs)
			if err != nil {
				return receipt, err
			}
			return o.dispatcher.SendTo(ctx, target, event.Outputs)
		case delivery.StreamSend:
			defer func() { o.observeDelivery(ctx, "send_assistant_message", receipt, err) }()
			event.Text, err = o.hooks.AgentOutputPrepared(ctx, event.Text)
			if err != nil {
				return receipt, err
			}
			defer func() {
				if err == nil && strings.TrimSpace(event.Text) != "" {
					o.hooks.PlatformMessageSent(ctx, event.Text)
				}
			}()
			if buffered {
				return o.dispatcher.SendTo(ctx, delivery.Target{}, []delivery.Output{{Kind: delivery.KindText, Text: event.Text, Actions: event.Actions}})
			}
		}
		if platformStream == nil {
			platformStream, err = o.dispatcher.StartStream(ctx)
			if err != nil {
				return receipt, err
			}
		}
		return platformStream.Send(ctx, event)
	})
}

// NewOutputSender initializes OutputSender without installing cross-service callbacks.
func NewOutputSender(deps OutputSenderDeps) *OutputSender {
	return &OutputSender{
		dispatcher: deps.Dispatcher,
		hooks:      deps.Hooks,
	}
}
