package input

import (
	"context"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

func (h *MessageHandler) messageWakeup(ctx context.Context, text string) bool {
	msg, ok := platform.MessageContextFrom(ctx)
	if !ok {
		return true
	}
	if msg.Conversation.Source.ConversationKind == "" || msg.Conversation.Source.ConversationKind == contextinfo.ConversationUnknown || msg.Conversation.Source.ConversationKind == contextinfo.ConversationPrivate {
		return true
	}
	if h.commands != nil && h.commands.IsCommand(text) {
		return true
	}
	if _, ok := platform.StripTriggerKeyword(text, msg.TriggerKeywords); ok {
		return true
	}
	if mentionsBot(msg) {
		return true
	}
	return h.isReplyToBot(ctx, msg)
}

func mentionsBot(msg platform.MessageContext) bool {
	botUserID := strings.TrimSpace(msg.Bot.UserID)
	botUsername := strings.TrimPrefix(strings.TrimSpace(msg.Bot.Username), "@")
	for _, mention := range msg.Mentions {
		if botUserID != "" && strings.TrimSpace(mention.UserID) == botUserID {
			return true
		}
		if botUsername != "" && strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(mention.Username), "@"), botUsername) {
			return true
		}
	}
	return false
}

func (h *MessageHandler) isReplyToBot(ctx context.Context, msg platform.MessageContext) bool {
	if strings.TrimSpace(msg.ReplyToSenderID) != "" && strings.TrimSpace(msg.ReplyToSenderID) == strings.TrimSpace(msg.Bot.UserID) {
		return true
	}
	replyID := strings.TrimSpace(msg.ReplyToMessageID)
	if replyID == "" || h.messages == nil {
		return false
	}
	mapped, err := h.messages.FindByPlatformMessage(ctx, msg.Conversation.Source.Platform, msg.Conversation.Source.ScopeID, replyID)
	if err == nil && mapped.Role == storage.RoleAssistant {
		return true
	}
	if h.mediaRows != nil {
		outputs, err := h.mediaRows.FindOutputs(ctx, msg.Conversation.Source.Platform, msg.Conversation.Source.ScopeID, replyID, storage.Now())
		return err == nil && len(outputs) > 0
	}
	return false
}
