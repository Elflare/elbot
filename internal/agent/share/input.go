package share

import (
	"context"
	"errors"
	"strings"

	"elbot/internal/platform"
)

// InputDisposition directs prepared input to execution or risk confirmation.
// It does not transfer ownership of the active Turn or its confirmation state.
type InputDisposition uint8

const (
	InputHandled InputDisposition = iota
	InputRiskConfirmation
)

var ErrInputCompacting = errors.New("正在压缩上下文，请稍后再发送。可使用 /stop 取消当前请求。")

var ErrInputArchived = errors.New("当前会话已归档，不能继续聊天。若要继续，请先使用 /unarchive。")

func StripWakeupPrefix(ctx context.Context, text string) string {
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
