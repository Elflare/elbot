package format

import (
	"fmt"
	"strings"

	"elbot/internal/control"
)

func Usage(d control.QueryUsageResult) string {
	var out strings.Builder
	if len(d.Items) == 0 && d.Total.Calls == 0 {
		out.WriteString("-\n")
	} else {
		fmt.Fprintf(&out, "usage · by=%s\n", d.GroupBy)
		for _, item := range d.Items {
			formatUsageBucket(&out, item)
		}
		formatUsageBucket(&out, d.Total)
	}
	if d.RangeTruncated {
		fmt.Fprintf(&out, "仅有 %d 次调用\n", d.AvailableCalls)
	}
	if d.DamagedCount > 0 {
		fmt.Fprintf(&out, "warn: damaged=%d · usage incomplete", d.DamagedCount)
	}
	return strings.TrimRight(out.String(), "\n")
}

func formatUsageBucket(out *strings.Builder, item control.UsageBucket) {
	fmt.Fprintf(out, "[%s] calls: %d\n  prompt: %s | completion: %s | total: %s\n", item.Key, item.Calls, number(item.PromptTokens), number(item.CompletionTokens), number(item.TotalTokens))
	cache := number(item.CacheHitTokens)
	if item.CacheHitTokens != nil && *item.CacheHitTokens >= 0 && item.PromptTokens != nil && *item.PromptTokens > 0 {
		cache += fmt.Sprintf(" (%.1f%%)", float64(*item.CacheHitTokens)/float64(*item.PromptTokens)*100)
	}
	fmt.Fprintf(out, "  cache: %s | elapsed: %s\n", cache, elapsed(item.ElapsedMS))
}

func elapsed(ms *int64) string {
	if ms == nil || *ms < 0 {
		return "unk"
	}
	return fmt.Sprintf("%.1fs", float64(*ms)/1000)
}

func number(value *int64) string {
	if value == nil || *value < 0 {
		return "unk"
	}
	return fmt.Sprint(*value)
}
