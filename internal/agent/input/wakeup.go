package input

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

func (h *MessageHandler) messageWakeup(ctx context.Context, text string) bool {
	msg, ok := agentshare.MessageContextFrom(ctx)
	if !ok {
		return true
	}
	info, _ := contextinfo.ConversationFromContext(ctx)
	if info.Source.ChatType != contextinfo.ChatGroup && info.Source.ChatType != contextinfo.ChatChannel {
		return true
	}
	if h.commands.IsCommand(text) {
		return true
	}
	if _, ok := agentshare.StripTriggerKeyword(text, msg.TriggerKeywords); ok {
		return true
	}
	if mentionsBot(msg) {
		return true
	}
	return h.isReplyToBot(ctx, msg)
}

func mentionsBot(msg agentshare.MessageContext) bool {
	botUserID := msg.Bot.UserID
	botUsername := strings.TrimPrefix(strings.TrimSpace(msg.Bot.Username), "@")
	for _, mention := range msg.Mentions {
		if botUserID != "" && mention.UserID == botUserID {
			return true
		}
		if botUsername != "" && strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(mention.Username), "@"), botUsername) {
			return true
		}
	}
	return false
}

func (h *MessageHandler) isReplyToBot(ctx context.Context, msg agentshare.MessageContext) bool {
	info, _ := contextinfo.ConversationFromContext(ctx)
	if info.ReplyToSenderID != "" && info.ReplyToSenderID == strings.TrimSpace(msg.Bot.UserID) {
		return true
	}
	replyID := info.ReplyToMessageID
	if replyID == "" || h.messages == nil {
		return false
	}
	mapped, err := h.messages.FindByPlatformMessage(ctx, info.Source.Platform, info.Source.ChatType, info.Source.ChatID, replyID)
	if err == nil && mapped.Role == storage.RoleAssistant {
		return true
	}
	if h.mediaRows != nil {
		outputs, err := h.mediaRows.FindOutputs(ctx, info.Source.Platform, info.Source.ChatType, info.Source.ChatID, replyID, storage.Now())
		return err == nil && len(outputs) > 0
	}
	return false
}
