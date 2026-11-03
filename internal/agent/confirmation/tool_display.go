package confirmation

import (
	"bytes"
	"encoding/json"
	"strings"
)

func riskReasonsText(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nrisk:")
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason != "" {
			sb.WriteString("\n- ")
			sb.WriteString(reason)
		}
	}
	return sb.String()
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

// previewArguments 截断已由 compactArguments 压缩的参数。
func previewArguments(args string) string {
	const maxPreviewRunes = 160
	if len([]rune(args)) <= maxPreviewRunes {
		return args
	}
	runes := []rune(args)
	return string(runes[:maxPreviewRunes]) + "..."
}
