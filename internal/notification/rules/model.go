package rules

import (
	"elbot/internal/llm"
	"fmt"
	"time"
)

func ModelRetry(provider string, event llm.RetryEvent) string {
	if event.Err == nil {
		return ""
	}
	if provider != "" {
		return fmt.Sprintf("LLM retry %d/%d · %s · %s 后: %v", event.Attempt, event.MaxRetries, provider, event.Delay.Round(time.Millisecond), event.Err)
	}
	return fmt.Sprintf("LLM retry %d/%d · %s 后: %v", event.Attempt, event.MaxRetries, event.Delay.Round(time.Millisecond), event.Err)
}
