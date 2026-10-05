package agent

import (
	"context"

	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

// AdoptForeground is installed by app as Session's synchronous execution participant.
func (a *Agent) AdoptForeground(ctx context.Context, row *storage.Session, binding *session.Binding) {
	if execution := a.turns.Execution(row.ID); execution != nil {
		ctx = security.WithActor(ctx, a.identity.Actor(ctx))
		execution.Adopt(session.WithBinding(ctx, binding))
	}
}

const foregroundInstructions = "此会话已由用户接管，当前是普通前台对话。保留原任务目标和历史，但后台无人值守、自动汇报及强制 JSON 输出要求已经解除；按当前用户要求正常回复，工具遵循前台权限和确认规则。"

func withForegroundInstructions(messages []llm.LLMMessage) []llm.LLMMessage {
	for _, message := range messages {
		if message.Role == llm.RoleSystem && llm.SegmentsTextOnly(message.Segments) == foregroundInstructions {
			return messages
		}
	}
	return append(messages, llm.LLMMessage{Role: llm.RoleSystem, Segments: llm.TextSegments(foregroundInstructions)})
}
