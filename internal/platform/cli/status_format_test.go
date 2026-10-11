package cli

import (
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"strings"
	"testing"
	"time"
)

func TestFormatUsage(t *testing.T) {
	total, cache := int64(18240), int64(12100)
	got := FormatUsage(&llm.Usage{TotalTokens: &total, CacheHitTokens: &cache})
	want := "tokens 18,240 · cache 12,100"
	if got != want {
		t.Fatalf("usage = %q, want %q", got, want)
	}
	if got := FormatUsage(nil); got != "" {
		t.Fatalf("nil usage = %q, want empty", got)
	}
	zero := int64(0)
	for _, tc := range []struct {
		usage llm.Usage
		want  string
	}{
		{llm.Usage{}, "tokens unknown · cache unknown"},
		{llm.Usage{TotalTokens: &zero, CacheHitTokens: &zero}, "tokens 0 · cache 0"},
		{llm.Usage{CacheHitTokens: &cache}, "tokens unknown · cache 12,100"},
		{llm.Usage{TotalTokens: &total}, "tokens 18,240 · cache unknown"},
	} {
		if got := FormatUsage(&tc.usage); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestFormatCompact(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	total, cache := int64(1234), int64(100)
	s := runtimestatus.Snapshot{Phase: runtimestatus.PhaseLLM, Provider: "deepseek", Model: "reasoner", TurnStartedAt: start, StageStartedAt: start, Usage: &llm.Usage{TotalTokens: &total, CacheHitTokens: &cache}}
	got := FormatCompact(s, start.Add(8*time.Second), 120)
	for _, want := range []string{"llm", "00:08", "deepseek/reasoner", "tokens 1,234", "cache 100"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status %q missing %q", got, want)
		}
	}
}

func TestFormatCompactTruncates(t *testing.T) {
	s := runtimestatus.Snapshot{Phase: runtimestatus.PhaseError, Error: strings.Repeat("x", 80)}
	got := FormatCompact(s, time.Now(), 16)
	if len([]rune(got)) > 16 {
		t.Fatalf("status length = %d, want <= 16: %q", len([]rune(got)), got)
	}
}
