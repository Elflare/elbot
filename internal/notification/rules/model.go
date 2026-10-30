package rules

import (
	"elbot/internal/llm"
	"fmt"
	"time"
)

const VisionFallback = "vision → text fallback"

func ModelInterrupted(err error) string { return fmt.Sprintf("LLM interrupted: %v", err) }
func ModelRetry(provider string, event llm.RetryEvent) string {
	if event.Err == nil {
		return ""
	}
	if provider != "" {
		return fmt.Sprintf("LLM retry %d/%d · %s · %s 后: %v", event.Attempt, event.MaxRetries, provider, event.Delay.Round(time.Millisecond), event.Err)
	}
	return fmt.Sprintf("LLM retry %d/%d · %s 后: %v", event.Attempt, event.MaxRetries, event.Delay.Round(time.Millisecond), event.Err)
}
