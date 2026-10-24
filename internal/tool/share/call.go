// 定义单次工具调用的请求与结果，提供结果的纯数据辅助。
package share

import (
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// TruncateLogContent bounds a log-only string to a positive Unicode character
// limit, including the ellipsis. Callers choose the content and limit; business
// results and execution arguments must remain unchanged.
func TruncateLogContent(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}

type CallRequest struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type Result struct {
	// NeedsElyphRules declares a language requirement of this result's content.
	NeedsElyphRules bool
	// DiscoveryText retains the insertion boundary between summary and details.
	DiscoveryText *DiscoveryText
	// Content is the text returned to the LLM for normal tool calls.
	Content string
	// Segments is the multimodal result returned to the LLM. Tools must use typed
	// segments for images/files instead of relying on URL guessing.
	Segments []llm.MessageSegment
	// Warnings are appended to the LLM-visible tool result to nudge future tool use.
	Warnings []string
	// Data is reserved for discover_tool's DiscoveryResult, which Agent uses to
	// inject discovered schemas as top-level tools. Normal tool call results must
	// use Content or Segments instead.
	Data     json.RawMessage
	Metadata map[string]any
	Outputs  []delivery.Output
}

func (r *Result) LLMSegments() []llm.MessageSegment {
	if r == nil {
		return nil
	}
	if len(r.Segments) > 0 {
		return llm.AppendSegmentText(r.Segments, warningsSuffix(r.Warnings))
	}
	return llm.TextSegments(AppendWarnings(r.Content, r.Warnings))
}

func AppendWarnings(content string, warnings []string) string {
	warningText := FormatWarnings(warnings)
	if warningText == "" {
		return content
	}
	content = strings.TrimRight(content, "\n")
	if strings.TrimSpace(content) == "" {
		return warningText
	}
	return content + "\n\n" + warningText
}

func FormatWarnings(warnings []string) string {
	warnings = normalizeWarnings(warnings)
	if len(warnings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Warnings:\n")
	for _, warning := range warnings {
		b.WriteString("- ")
		b.WriteString(strings.ReplaceAll(warning, "\n", "\n  "))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func warningsSuffix(warnings []string) string {
	warningText := FormatWarnings(warnings)
	if warningText == "" {
		return ""
	}
	return "\n\n" + warningText
}

func normalizeWarnings(warnings []string) []string {
	out := make([]string, 0, len(warnings))
	seen := map[string]bool{}
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" || seen[warning] {
			continue
		}
		seen[warning] = true
		out = append(out, warning)
	}
	return out
}
