package cli

import (
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

func FormatCompact(s runtimestatus.Snapshot, now time.Time, width int) string {
	parts := []string{}
	phase := string(s.Phase)
	if phase == "" {
		phase = string(runtimestatus.PhaseIdle)
	}
	if s.Phase == runtimestatus.PhaseTool && s.ToolName != "" {
		phase += " " + s.ToolName
	} else if s.Phase == runtimestatus.PhaseError && s.Error != "" {
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
	if usage == nil {
		return ""
	}
	text := "tokens unknown"
	if usage.TotalTokens != nil && *usage.TotalTokens >= 0 {
		text = "tokens " + formatInt(*usage.TotalTokens)
	}
	if usage.CacheHitTokens != nil && *usage.CacheHitTokens >= 0 {
		text += " · cache " + formatInt(*usage.CacheHitTokens)
	} else {
		text += " · cache unknown"
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

func formatInt(n int64) string {
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
