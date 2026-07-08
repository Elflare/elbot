package agent

import (
	"context"
	"strings"

	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

func (a *Agent) hookWakeup(ctx context.Context, event hook.Event) bool {
	text := strings.TrimSpace(llm.SegmentsTextOnly(event.Message.Segments))
	if text == "" {
		text = inboundRawText(ctx)
	}
	return a.messageWakeup(ctx, text)
}

func (a *Agent) messageWakeup(ctx context.Context, text string) bool {
	msg, ok := platform.MessageContextFrom(ctx)
	if !ok {
		return true
	}
	if msg.ConversationKind == "" || msg.ConversationKind == platform.ConversationUnknown || msg.ConversationKind == platform.ConversationPrivate {
		return true
	}
	if a.commands != nil && a.commands.IsCommand(text) {
		return true
	}
	if _, ok := platform.StripTriggerKeyword(text, msg.TriggerKeywords); ok {
		return true
	}
	if mentionsBot(msg) {
		return true
	}
	return a.isReplyToBot(ctx, msg)
}

func (a *Agent) stripWakeupPrefix(ctx context.Context, text string) string {
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		return stripWakeupPrefixFromText(text, msg)
	}
	return text
}

func stripWakeupPrefixFromText(text string, msg platform.MessageContext) string {
	if stripped, ok := platform.StripTriggerKeyword(text, msg.TriggerKeywords); ok {
		return stripped
	}
	if before, after, ok := strings.Cut(text, "\n\n"); ok {
		stripped := stripWakeupPrefixFromText(after, msg)
		if stripped != after {
			return before + "\n\n" + stripped
		}
	}
	return stripBotMention(text, msg)
}

func inboundRawText(ctx context.Context) string {
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		return strings.TrimSpace(msg.RawText)
	}
	return ""
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

func stripBotMention(text string, msg platform.MessageContext) string {
	botUsername := strings.TrimPrefix(strings.TrimSpace(msg.Bot.Username), "@")
	if botUsername == "" || text == "" {
		return text
	}
	fields := strings.Fields(text)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "/") {
		if name, mention, ok := strings.Cut(fields[0], "@"); ok && strings.EqualFold(mention, botUsername) {
			fields[0] = name
			return strings.TrimSpace(strings.Join(fields, " "))
		}
	}
	kept := fields[:0]
	for _, field := range fields {
		if strings.EqualFold(field, "@"+botUsername) {
			continue
		}
		kept = append(kept, field)
	}
	return strings.TrimSpace(strings.Join(kept, " "))
}

func (a *Agent) isReplyToBot(ctx context.Context, msg platform.MessageContext) bool {
	if strings.TrimSpace(msg.ReplyToSenderID) != "" && strings.TrimSpace(msg.ReplyToSenderID) == strings.TrimSpace(msg.Bot.UserID) {
		return true
	}
	replyID := strings.TrimSpace(msg.ReplyToMessageID)
	if replyID == "" || a.store == nil || a.store.Messages() == nil {
		return false
	}
	mapped, err := a.store.Messages().FindByPlatformMessage(ctx, msg.Platform, msg.ScopeID, replyID)
	return err == nil && mapped.Role == storage.RoleAssistant
}
