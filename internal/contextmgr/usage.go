package contextmgr

import (
	"fmt"

	"elbot/internal/llm"
)

type UsageState struct {
	Usage          *llm.Usage
	ContextWindow  int
	TriggerRatio   float64
	PendingCompact bool
}

func (s UsageState) TokensKnown() bool {
	return s.Usage != nil && s.Usage.TotalTokens != nil && *s.Usage.TotalTokens >= 0
}

func (s UsageState) UsageRatio() float64 {
	if !s.TokensKnown() || s.ContextWindow <= 0 {
		return 0
	}
	return float64(*s.Usage.TotalTokens) / float64(s.ContextWindow)
}

func (s UsageState) ReachedThreshold() bool {
	return s.TokensKnown() && s.TriggerRatio > 0 && s.UsageRatio() >= s.TriggerRatio
}

func FormatTokens(usage *llm.Usage) string {
	if usage == nil {
		return "tokens=unk · cache=unk"
	}
	tokens, cache := "unk", "unk"
	if usage.TotalTokens != nil && *usage.TotalTokens >= 0 {
		tokens = fmt.Sprint(*usage.TotalTokens)
	}
	if usage.CacheHitTokens != nil && *usage.CacheHitTokens >= 0 {
		cache = fmt.Sprint(*usage.CacheHitTokens)
		if usage.PromptTokens != nil && *usage.PromptTokens > 0 {
			cache += fmt.Sprintf(" (%.1f%%)", float64(*usage.CacheHitTokens)/float64(*usage.PromptTokens)*100)
		}
	}
	return fmt.Sprintf("tokens=%s · cache=%s", tokens, cache)
}
