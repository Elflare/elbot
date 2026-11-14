package confirmation

import "strings"

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
