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
	if usage.TotalTokens == nil || *usage.TotalTokens < 0 {
		if usage.CacheHitTokens == nil || *usage.CacheHitTokens < 0 {
			return "tokens=unk · cache=unk"
		}
		return fmt.Sprintf("tokens=unk · cache=%d", *usage.CacheHitTokens)
	}
	if usage.CacheHitTokens == nil || *usage.CacheHitTokens < 0 {
		return fmt.Sprintf("tokens=%d · cache=unk", *usage.TotalTokens)
	}
	return fmt.Sprintf("tokens=%d · cache=%d", *usage.TotalTokens, *usage.CacheHitTokens)
}
