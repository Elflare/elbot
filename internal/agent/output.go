package agent

import (
	"context"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

func (a *Agent) sendOutputs(ctx context.Context, outputs []delivery.Output) error {
	_, err := a.dispatcher.SendNotice(ctx, delivery.Notice{Outputs: outputs})
	return err
}
func (a *Agent) sendChat(ctx context.Context, text string) {
	_, _ = a.sendChatWithReceipt(ctx, text)
}

func (a *Agent) sendNotice(ctx context.Context, notice delivery.Notice) error {
	_, err := a.dispatcher.SendNotice(ctx, notice)
	return err
}

func (a *Agent) prepareAssistantOutput(ctx context.Context, point hook.Point, text string) (string, error) {
	event, err := a.runHook(ctx, hook.Event{Point: point, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(text)}})
	if err != nil {
		return "", err
	}
	return llm.SegmentsTextOnly(event.Message.Segments), nil
}

func (a *Agent) sendChatWithReceipt(ctx context.Context, text string) (delivery.Receipt, error) {
	if strings.TrimSpace(text) == "" && bufferAssistantOutput(ctx) {
		return delivery.Receipt{}, nil
	}
	preparedText, err := a.prepareAssistantOutput(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return delivery.Receipt{}, err
	}
	receipt, err := a.dispatcher.SendChat(ctx, []delivery.Output{delivery.Text(preparedText)})
	if err != nil {
		if a.logger != nil {
			a.logger.WarnContext(ctx, "chat send failed", "error", err.Error())
		}
		return receipt, err
	}
	a.notifyHook(ctx, hook.Event{Point: hook.PointPlatformMessageSent, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(preparedText)}})

	return receipt, nil
}

func bufferAssistantOutput(ctx context.Context) bool {
	msg, ok := platform.MessageContextFrom(ctx)
	return ok && msg.BufferAssistantOutput
}

func (a *Agent) mapSentAssistantMessage(ctx context.Context, sessionID, messageID string, receipt delivery.Receipt) {
	if len(receipt.PlatformMessageIDs) == 0 || a.store == nil || a.store.Messages() == nil {
		return
	}
	scope := a.scope(ctx)
	for _, platformMessageID := range receipt.PlatformMessageIDs {
		platformMessageID = strings.TrimSpace(platformMessageID)
		if platformMessageID == "" {
			continue
		}
		mapping := storage.PlatformMessageMap{
			Platform:          scope.Platform,
			PlatformScopeID:   scope.PlatformScopeID,
			PlatformMessageID: platformMessageID,
			MessageID:         messageID,
			SessionID:         sessionID,
		}
		if err := a.store.Messages().MapPlatformMessage(ctx, mapping); err != nil {
			a.audit("persistence_error", "session_id", sessionID, "operation", "map_platform_message", "platform_message_id", platformMessageID, "error", err.Error())
			if a.logger != nil {
				a.logger.WarnContext(ctx, "map platform message failed", "session_id", sessionID, "platform_message_id", platformMessageID, "error", err.Error())
			}
		}
	}
}
