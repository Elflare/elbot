package runtime

import (
	"strings"
	"testing"
	"time"

	"elbot/internal/llm"
)

func TestSnapshotElapsedUsesFinishedAt(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Snapshot{TurnStartedAt: start, StageStartedAt: start.Add(time.Second), FinishedAt: start.Add(65 * time.Second)}
	if got := FormatDuration(s.Elapsed(start.Add(2 * time.Minute))); got != "01:05" {
		t.Fatalf("elapsed = %s, want 01:05", got)
	}
}

func TestFormatUsage(t *testing.T) {
	got := FormatUsage(&llm.Usage{TotalTokens: 18240, CacheHitTokens: 12100})
	want := "tokens 18,240 · cache 12,100"
	if got != want {
		t.Fatalf("usage = %q, want %q", got, want)
	}
	if got := FormatUsage(nil); got != "" {
		t.Fatalf("nil usage = %q, want empty", got)
	}
}

func TestFormatCompact(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Snapshot{Phase: PhaseLLM, Provider: "deepseek", Model: "reasoner", TurnStartedAt: start, StageStartedAt: start, Usage: &llm.Usage{TotalTokens: 1234, CacheHitTokens: 100}}
	got := FormatCompact(s, start.Add(8*time.Second), 120)
	for _, want := range []string{"llm", "00:08", "deepseek/reasoner", "tokens 1,234", "cache 100"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status %q missing %q", got, want)
		}
	}
}

func TestFormatCompactTruncates(t *testing.T) {
	s := Snapshot{Phase: PhaseError, Error: strings.Repeat("x", 80)}
	got := FormatCompact(s, time.Now(), 16)
	if len([]rune(got)) > 16 {
		t.Fatalf("status length = %d, want <= 16: %q", len([]rune(got)), got)
	}
}
