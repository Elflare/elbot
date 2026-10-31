// 定义单次工具调用的请求与结果，提供结果的纯数据辅助。
package share

import (
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"encoding/json"
	"strings"
)

// AbsolutePathWorkspaceWarning 由工具在前台使用绝对路径时附加到结果。
const AbsolutePathWorkspaceWarning = "使用了绝对路径，不会改变 workspace。若多次在该目录工作，建议先使用 workspace 工具切换。"

// TruncateLogContent bounds a log-only string to a positive Unicode character
// limit, including the ellipsis. Callers choose the content and limit; business
// results and execution arguments must remain unchanged.
func TruncateLogContent(text string, limit int) string {
	return logging.TruncateContent(text, limit)
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

// LogContent returns a log-only preview of the actual result and error, bounded
// to 1024 Unicode characters including the ellipsis. Nil means no result; tool
// warnings and the independent summary are not part of this preview.
func (r *Result) LogContent(err error) string {
	body := ""
	if r != nil {
		body = r.Content
		if len(r.Segments) > 0 {
			body = llm.SegmentsRawContentText(r.Segments)
		}
	}
	if err != nil {
		if body != "" {
			body = err.Error() + "\n" + body
		} else {
			body = err.Error()
		}
	}
	return TruncateLogContent(body, 1024)
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
