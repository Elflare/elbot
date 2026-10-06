package agent

import (
	"bytes"
	"encoding/json"
	"strings"

	"elbot/internal/llm"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
)

func riskReasonsText(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n风险原因：")
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason != "" {
			sb.WriteString("\n- ")
			sb.WriteString(reason)
		}
	}
	return sb.String()
}

func previewLogText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	const maxPreviewRunes = 120
	if len([]rune(text)) <= maxPreviewRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxPreviewRunes]) + "..."
}

func joinToolNames(calls []llm.ToolCallRequest) string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		if call.Name != "" {
			names = append(names, call.Name)
		}
	}
	return strings.Join(names, ", ")
}

func compactArguments(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "{}"
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(args)); err == nil {
		return compact.String()
	}
	return args
}

func previewArguments(args string) string {
	args = compactArguments(args)
	const maxPreviewRunes = 160
	if len([]rune(args)) <= maxPreviewRunes {
		return args
	}
	runes := []rune(args)
	return string(runes[:maxPreviewRunes]) + "..."
}

func isBackgroundSession(row *storage.Session) bool { return sessionpkg.IsBackground(row) }
