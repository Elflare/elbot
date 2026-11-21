package chat

import (
	"fmt"

	"elbot/internal/llm"
)

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
