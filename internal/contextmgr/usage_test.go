package contextmgr

import (
	"testing"

	"elbot/internal/llm"
)

func TestUsageStateAndFormatTokens(t *testing.T) {
	state := UsageState{Usage: &llm.Usage{TotalTokens: 90, CacheHitTokens: 10}, ContextWindow: 100, TriggerRatio: 0.8}
	if !state.ReachedThreshold() {
		t.Fatal("expected threshold reached")
	}
	if got := FormatTokens(state.Usage); got != "tokens：90（命中：10）" {
		t.Fatalf("tokens = %q", got)
	}
	if got := FormatTokens(nil); got != "tokens：unknown（命中：unknown）" {
		t.Fatalf("unknown tokens = %q", got)
	}
}
