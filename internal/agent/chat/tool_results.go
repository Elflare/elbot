package chat

import (
	"fmt"
	"strings"

	"elbot/internal/agent/dialogue"
	"elbot/internal/llm"
	"elbot/internal/turn"
)

func drainPendingUserInput(turns *turn.Manager, id string, messages []llm.LLMMessage, expected ...string) ([]llm.LLMMessage, *dialogue.PendingUserMessage) {
	pending := dialogue.DrainPending(turns, id, expected...)
	if pending == nil {
		return messages, nil
	}
	pending.MessageIndex = len(messages)
	return append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: pending.Segments}), pending
}
func skippedToolMessages(calls []llm.ToolCallRequest, maxRounds int) []llm.LLMMessage {
	messages := make([]llm.LLMMessage, 0, len(calls))
	for _, call := range calls {
		messages = append(messages, llm.LLMMessage{
			Role:       llm.RoleTool,
			Name:       call.Name,
			ToolCallID: call.ID,
			Segments:   llm.TextSegments(fmt.Sprintf("tool call skipped: max_rounds_per_turn=%d reached. Please summarize current progress without calling more tools.", maxRounds)),
		})
	}
	return messages
}

func joinAssistantText(first, second string) string {
	first = strings.TrimSpace(first)
	second = strings.TrimSpace(second)
	switch {
	case first == "":
		return second
	case second == "":
		return first
	default:
		return first + "\n\n" + second
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
