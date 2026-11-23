package chat

import "fmt"

func (s *chatTurnState) skippedToolText(maxRounds int) string {
	s.output.SendPreview(s.Ctx, fmt.Sprintf("max_rounds_per_turn=%d → 跳过后续工具，生成总结", maxRounds))
	return fmt.Sprintf("Skipped: max_rounds_per_turn=%d reached. Summarize progress; no more tools.", maxRounds)
}
