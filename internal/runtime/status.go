package runtime

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"elbot/internal/llm"
	"elbot/internal/request"
)

type Phase string

const (
	PhaseIdle      Phase = "idle"
	PhasePreparing Phase = "preparing"
	PhaseLLM       Phase = "llm"
	PhaseTool      Phase = "tool"
	PhaseSending   Phase = "sending"
	PhaseDone      Phase = "done"
	PhaseError     Phase = "error"
)

type Snapshot struct {
	SessionID string
	Phase     Phase

	Provider string
	Model    string
	Mode     string

	RequestID string
	Kind      request.Kind
	Label     string
	ToolName  string

	TurnStartedAt  time.Time
	StageStartedAt time.Time
	FinishedAt     time.Time

	Usage *llm.Usage
	Error string
}

func (s Snapshot) Running() bool {
	return !s.StageStartedAt.IsZero() && s.FinishedAt.IsZero() && s.Phase != PhaseIdle && s.Phase != PhaseDone && s.Phase != PhaseError
}

func (s Snapshot) Elapsed(now time.Time) time.Duration {
	start := s.TurnStartedAt
	if start.IsZero() {
		start = s.StageStartedAt
	}
	if start.IsZero() {
		return 0
	}
	end := s.FinishedAt
	if end.IsZero() {
		end = now
	}
	if end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

func (s Snapshot) StageElapsed(now time.Time) time.Duration {
	if s.StageStartedAt.IsZero() {
		return s.Elapsed(now)
	}
	end := s.FinishedAt
	if end.IsZero() {
		end = now
	}
	if end.Before(s.StageStartedAt) {
		return 0
	}
	return end.Sub(s.StageStartedAt)
}

func FormatCompact(s Snapshot, now time.Time, width int) string {
	parts := []string{}
	phase := string(s.Phase)
	if phase == "" {
		phase = string(PhaseIdle)
	}
	if s.Phase == PhaseTool && s.ToolName != "" {
		phase += " " + s.ToolName
	} else if s.Phase == PhaseError && s.Error != "" {
		phase += " " + shortText(s.Error, 40)
	}
	parts = append(parts, phase)
	if d := s.Elapsed(now); d > 0 {
		parts = append(parts, FormatDuration(d))
	}
	if model := FormatModel(s.Provider, s.Model); model != "" {
		parts = append(parts, model)
	}
	if token := FormatUsage(s.Usage); token != "" {
		parts = append(parts, token)
	}
	if len(parts) == 0 {
		return "idle"
	}
	return truncateRunes(strings.Join(parts, " · "), width)
}

func FormatModel(provider, model string) string {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	switch {
	case provider != "" && model != "":
		return provider + "/" + model
	case model != "":
		return model
	default:
		return provider
	}
}

func FormatUsage(usage *llm.Usage) string {
	if usage == nil || usage.TotalTokens <= 0 {
		return ""
	}
	text := "tokens " + formatInt(usage.TotalTokens)
	if usage.CacheHitTokens > 0 {
		text += " · cache " + formatInt(usage.CacheHitTokens)
	}
	return text
}

func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Round(time.Second).Seconds())
	if seconds < 3600 {
		return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
}

func formatInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}

func shortText(text string, maxRunes int) string {
	return truncateRunes(strings.TrimSpace(text), maxRunes)
}

func truncateRunes(text string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "…"
}
