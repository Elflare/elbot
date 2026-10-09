package contextmgr

import (
	"testing"

	"elbot/internal/llm"
)

func TestUsageStateAndFormatTokens(t *testing.T) {
	total, cache := int64(90), int64(10)
	state := UsageState{Usage: &llm.Usage{TotalTokens: &total, CacheHitTokens: &cache}, ContextWindow: 100, TriggerRatio: 0.8}
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

func TestUsageUnknownAndZero(t *testing.T) {
	zero, cache, negative := int64(0), int64(3), int64(-1)
	for _, tc := range []struct {
		usage *llm.Usage
		known bool
		text  string
	}{
		{nil, false, "tokens：unknown（命中：unknown）"},
		{&llm.Usage{}, false, "tokens：unknown（命中：unknown）"},
		{&llm.Usage{CacheHitTokens: &cache}, false, "tokens：unknown（命中：3）"},
		{&llm.Usage{TotalTokens: &zero}, true, "tokens：0（命中：unknown）"},
		{&llm.Usage{TotalTokens: &zero, CacheHitTokens: &zero}, true, "tokens：0（命中：0）"},
		{&llm.Usage{TotalTokens: &negative, CacheHitTokens: &cache}, false, "tokens：unknown（命中：3）"},
	} {
		state := UsageState{Usage: tc.usage, ContextWindow: 100, TriggerRatio: 0.8}
		if state.TokensKnown() != tc.known || state.ReachedThreshold() || state.UsageRatio() != 0 {
			t.Fatalf("incorrect known/threshold/ratio for %#v", tc.usage)
		}
		if got := FormatTokens(tc.usage); got != tc.text {
			t.Errorf("got %q, want %q", got, tc.text)
		}
	}
}
