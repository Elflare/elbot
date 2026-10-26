package chat

import (
	agentshare "elbot/internal/agent/share"

	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/turn"
)

func drainPendingUserInput(turns interface {
	DrainMergedInput(string, ...string) turn.Input
}, id string, messages []llm.LLMMessage, expected ...string) ([]llm.LLMMessage, *agentshare.PendingUserMessage) {
	pending := agentshare.DrainPending(turns, id, expected...)
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
