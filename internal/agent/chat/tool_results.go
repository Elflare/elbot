package chat

import (
	agentshare "elbot/internal/agent/share"

	"fmt"

	"elbot/internal/llm"
	"elbot/internal/turn"
)

func drainPendingUserInput(turns interface {
	DrainMergedInput(string, string) turn.Input
}, id string, messages []llm.LLMMessage, expected string) ([]llm.LLMMessage, *agentshare.PendingUserMessage) {
	pending := agentshare.DrainPending(turns, id, expected)
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
			Segments:   llm.TextSegments(fmt.Sprintf("Skipped: max_rounds_per_turn=%d reached. Summarize progress; no more tools.", maxRounds)),
		})
	}
	return messages
}
